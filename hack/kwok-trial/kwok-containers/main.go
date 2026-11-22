// Command kwok-containers spikes a "container node" for KWOK: pods scheduled to one node are
// run for real in Docker, on the cluster's network, while every other pod stays simulated.
// It installs cert-manager with Helm and checks that its controller issues a certificate,
// its webhook is reached through its Service, and the API server's pod proxy reaches it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	kwokImage   = "registry.k8s.io/kwok/cluster:v0.8.0-k8s.v1.36.1"
	nodeName    = "containers"
	taintKey    = "vela.test/containers"
	proberImage = "curlimages/curl:8.11.1"
	dnsImage    = "registry.k8s.io/coredns/coredns:v1.12.0"
	proxyImage  = "registry.k8s.io/kube-proxy:v1.36.1"
	toolImage   = "busybox:1.36"
)

var (
	netName     = "kc"
	serviceCIDR = "10.96.0.0/12"
	keep        = flag.Bool("keep", false, "leave everything running")
	version     = flag.String("cert-manager", "v1.21.3", "cert-manager chart version")
	dir         = filepath.Join(os.TempDir(), "kwok-containers")
)

const kwokctlConfig = `apiVersion: config.kwok.x-k8s.io/v1alpha1
kind: KwokctlConfiguration
componentsPatches:
- name: kube-apiserver
  extraArgs:
  - {key: service-cluster-ip-range, value: 10.96.0.0/12}
  - {key: enable-aggregator-routing, value: "true"}
  - {key: enable-admission-plugins, value: "PodNodeSelector,PodTolerationRestriction"}
- name: kube-controller-manager
  extraArgs:
  - {key: service-cluster-ip-range, value: 10.96.0.0/12}
- name: kwok-controller
  extraArgs:
  - {key: manage-all-nodes, value: "false", override: true}
  - {key: manage-nodes-with-annotation-selector, value: "kwok.x-k8s.io/node=fake"}
`

func main() {
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		attachMain(os.Args[2:])
		return
	}
	flag.Parse()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !*keep {
		defer down()
	}
	t0 := time.Now()
	cs, kc, kwokIP, err := up(ctx)
	if err != nil {
		fail("cluster: %v", err)
	}
	step(t0, "KWOK cluster up, container node %q Ready, kwok at %s", nodeName, kwokIP)
	t := time.Now()

	t = time.Now()
	dnsIP, proxyIP, err := network(fmt.Sprintf("https://%s-kwok:6443", netName))
	if err != nil {
		fail("network: %v", err)
	}
	step(t, "CoreDNS at %s, kube-proxy at %s", dnsIP, proxyIP)

	r := &runner{cs: cs, kwokIP: kwokIP, apiHost: kwokIP, apiPort: "6443", dnsIP: dnsIP, proxyIP: proxyIP, pods: map[types.UID]*podRun{}}
	go r.nodeHeartbeat(ctx)
	go r.watch(ctx)

	// Pods in this namespace go to the container node: built-in admission plugins read the
	// namespace's annotations, so nothing in the chart changes.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager", Annotations: map[string]string{
		"scheduler.alpha.kubernetes.io/node-selector":      "role=containers",
		"scheduler.alpha.kubernetes.io/defaultTolerations": `[{"key":"` + taintKey + `","operator":"Exists","effect":"NoSchedule"}]`,
	}}}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		fail("namespace: %v", err)
	}

	t = time.Now()
	out, err := cmd("helm", "--kubeconfig", kc, "install", "cert-manager", "cert-manager", "--repo", "https://charts.jetstack.io",
		"--version", *version, "-n", "cert-manager", "--set", "crds.enabled=true", "--wait", "--timeout", "5m")
	if err != nil {
		r.dump()
		fail("helm install: %v\n%s", err, out)
	}
	step(t, "helm install --wait passed: cert-manager %s running for real (hook Job included)", *version)
	r.summary()

	results := []check{
		{"a self-signed Certificate is issued by the real controller", func() error { return issued(kc) }},
		{"an invalid Certificate is refused by the real webhook, through its Service", func() error { return refused(kc) }},
		{"the API server's pod proxy reaches the webhook's /healthz", func() error { return proxied(cs, kc) }},
		{"a pod on the simulated node is unaffected", func() error { return simulated(kc) }},
		{"a real pod calls another by Service name and port, in its namespace and across", func() error { return services(cs, kc) }},
	}
	failed := 0
	for _, c := range results {
		t := time.Now()
		if err := c.run(); err != nil {
			failed++
			fmt.Printf("FAIL %s (%s)\n     %v\n", c.name, time.Since(t).Round(time.Second), err)
		} else {
			fmt.Printf("PASS %s (%s)\n", c.name, time.Since(t).Round(time.Second))
		}
	}
	fmt.Printf("\n%d passed, %d failed in %s\n", len(results)-failed, failed, time.Since(t0).Round(time.Second))
	if failed > 0 {
		r.dump()
		os.Exit(1)
	}
}

type check struct {
	name string
	run  func() error
}

// ---- cluster ----

func up(ctx context.Context) (*kubernetes.Clientset, string, string, error) {
	down()
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "kwokctl.yaml"), []byte(kwokctlConfig), 0o644); err != nil {
		return nil, "", "", err
	}
	if _, err := cmd("docker", "network", "create", netName); err != nil {
		return nil, "", "", err
	}
	if _, err := cmd("docker", "run", "-d", "--name", netName+"-kwok", "--network", netName, "-v", dir+":/vd:ro",
		"-p", "127.0.0.1::6443", "-e", "KWOK_KUBE_APISERVER_PORT=6443", kwokImage, "--config", "/vd/kwokctl.yaml"); err != nil {
		return nil, "", "", err
	}
	port, err := cmd("docker", "port", netName+"-kwok", "6443/tcp")
	if err != nil {
		return nil, "", "", err
	}
	port = strings.TrimSpace(strings.Split(port, "\n")[0])
	for i := 0; i < 120; i++ {
		if _, err := cmd("docker", "exec", netName+"-kwok", "sh", "-c", "test -f /root/.kwok/clusters/kwok/pki/admin.crt"); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, f := range []string{"admin.crt", "admin.key", "ca.crt"} {
		if _, err := cmd("docker", "cp", netName+"-kwok:/root/.kwok/clusters/kwok/pki/"+f, filepath.Join(dir, f)); err != nil {
			return nil, "", "", err
		}
	}
	kc := filepath.Join(dir, "admin.kubeconfig")
	_ = os.WriteFile(kc, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: kwok, cluster: {server: "https://%s", insecure-skip-tls-verify: true}}]
users: [{name: admin, user: {client-certificate: %s, client-key: %s}}]
contexts: [{name: kwok, context: {cluster: kwok, user: admin}}]
current-context: kwok
`, port, filepath.Join(dir, "admin.crt"), filepath.Join(dir, "admin.key"))), 0o600)
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		return nil, "", "", err
	}
	cfg.QPS, cfg.Burst = 100, 200
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "", "", err
	}
	if err := poll(60*time.Second, func() error { _, err := cs.Discovery().ServerVersion(); return err }); err != nil {
		return nil, "", "", fmt.Errorf("API server never answered: %v", err)
	}
	// One simulated node for everything else.
	if out, err := cmd("docker", "exec", netName+"-kwok", "kwokctl", "scale", "node", "--replicas", "1"); err != nil {
		return nil, "", "", fmt.Errorf("%v: %s", err, out)
	}
	ip, err := cmd("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", netName+"-kwok")
	if err != nil {
		return nil, "", "", err
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{"role": "containers", "kubernetes.io/hostname": nodeName, "kubernetes.io/os": "linux"}},
		Spec:       corev1.NodeSpec{Taints: []corev1.Taint{{Key: taintKey, Value: "true", Effect: corev1.TaintEffectNoSchedule}}},
	}
	if _, err := cs.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		return nil, "", "", err
	}
	return cs, kc, strings.TrimSpace(ip), nil
}

func down() {
	byNet, _ := cmd("docker", "ps", "-aq", "--filter", "network="+netName)
	byLabel, _ := cmd("docker", "ps", "-aq", "--filter", "label="+netLabel())
	if ids := strings.Fields(byNet + " " + byLabel); len(ids) > 0 {
		_, _ = cmd("docker", append([]string{"rm", "-f"}, ids...)...)
	}
	_, _ = cmd("docker", "network", "rm", netName)
}

// ---- the container node's kubelet ----

type runner struct {
	cs      *kubernetes.Clientset
	kwokIP  string // the container node's address
	apiHost string // what kubernetes.default.svc resolves to in a pod: an IP, or host-gateway
	apiPort string
	// nodeAddr is the address the API server reaches this node's kubelet API at, on
	// kubeletPort; zero means no kubelet API, and the node reports kwokIP.
	nodeAddr    string
	kubeletPort int32
	// nodeIP, when set, is the node's InternalIP: kube-proxy's address, where NodePorts
	// answer for anything on the cluster's Docker network.
	nodeIP  string
	dnsIP   string // CoreDNS, the real pods' resolver
	proxyIP string // kube-proxy, the real pods' route to Service IPs
	mu      sync.Mutex
	pods    map[types.UID]*podRun
}

type podRun struct {
	pod        *corev1.Pod
	containers []string          // docker names: the sandbox, then app containers in order
	byName     map[string]string // container name -> docker name
	ip         string
	started    time.Time
	err        string
	retryAt    time.Time // after a failed image pull, when to try again
}

// pullError is an image the node could not pull; the pod waits in ErrImagePull, as on a kubelet.
type pullError struct {
	container, image, msg string
}

func (e *pullError) Error() string { return fmt.Sprintf("pull %s: %s", e.image, e.msg) }

// pull fetches an image the host does not have yet.
func pull(c corev1.Container) error {
	if _, err := cmd("docker", "image", "inspect", c.Image); err == nil && c.ImagePullPolicy != corev1.PullAlways {
		return nil
	}
	if out, err := cmd("docker", "pull", "-q", c.Image); err != nil {
		return &pullError{container: c.Name, image: c.Image, msg: strings.TrimSpace(out)}
	}
	return nil
}

// nodeHeartbeat keeps the container node Ready, as a kubelet would.
func (r *runner) nodeHeartbeat(ctx context.Context) {
	for {
		now := metav1.Now()
		cpu, mem := "8", "16Gi"
		status := corev1.NodeStatus{
			Capacity:    corev1.ResourceList{"cpu": mustQ(cpu), "memory": mustQ(mem), "pods": mustQ("110")},
			Allocatable: corev1.ResourceList{"cpu": mustQ(cpu), "memory": mustQ(mem), "pods": mustQ("110")},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "ContainerNodeReady",
				LastHeartbeatTime: now, LastTransitionTime: now}},
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: r.kwokIP}},
		}
		if r.nodeIP != "" {
			status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: r.nodeIP}}
		}
		if r.kubeletPort != 0 {
			// The API server prefers a Hostname to an InternalIP when it calls a kubelet.
			status.Addresses = append([]corev1.NodeAddress{{Type: corev1.NodeHostName, Address: r.nodeAddr}}, status.Addresses...)
			status.DaemonEndpoints = corev1.NodeDaemonEndpoints{KubeletEndpoint: corev1.DaemonEndpoint{Port: r.kubeletPort}}
		}
		b, _ := json.Marshal(map[string]any{"status": status})
		_, _ = r.cs.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, b, metav1.PatchOptions{}, "status")
		lease, err := r.cs.CoordinationV1().Leases("kube-node-lease").Get(ctx, nodeName, metav1.GetOptions{})
		mt := metav1.NewMicroTime(time.Now())
		holder, dur := nodeName, int32(40)
		if apierrors.IsNotFound(err) {
			_, _ = r.cs.CoordinationV1().Leases("kube-node-lease").Create(ctx, &coordv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: coordv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &dur, RenewTime: &mt}}, metav1.CreateOptions{})
		} else if err == nil {
			lease.Spec.RenewTime = &mt
			_, _ = r.cs.CoordinationV1().Leases("kube-node-lease").Update(ctx, lease, metav1.UpdateOptions{})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// watch reconciles the pods bound to the container node.
func (r *runner) watch(ctx context.Context) {
	sel := fields.OneTermEqualSelector("spec.nodeName", nodeName).String()
	for ctx.Err() == nil {
		list, err := r.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: sel})
		if err == nil {
			seen := map[types.UID]bool{}
			for i := range list.Items {
				p := &list.Items[i]
				seen[p.UID] = true
				r.reconcile(ctx, p)
			}
			r.mu.Lock()
			for uid, pr := range r.pods {
				if !seen[uid] {
					stop(pr)
					delete(r.pods, uid)
				}
			}
			r.mu.Unlock()
		}
		time.Sleep(time.Second)
	}
}

func (r *runner) reconcile(ctx context.Context, p *corev1.Pod) {
	r.mu.Lock()
	pr := r.pods[p.UID]
	if pr == nil {
		pr = &podRun{pod: p}
		r.pods[p.UID] = pr
	}
	r.mu.Unlock()

	if p.DeletionTimestamp != nil {
		stop(pr)
		zero := int64(0)
		_ = r.cs.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &p.UID}})
		return
	}
	if len(pr.containers) == 0 {
		if time.Now().Before(pr.retryAt) {
			return
		}
		if err := r.start(ctx, p, pr); err != nil {
			var pe *pullError
			if errors.As(err, &pe) {
				pr.retryAt = time.Now().Add(10 * time.Second)
				r.reportPull(ctx, p, pe)
			}
			if pr.err != err.Error() {
				fmt.Printf("  pod %s/%s: not started yet: %v\n", p.Namespace, p.Name, err)
				pr.err = err.Error()
			}
			stop(pr)
			pr.containers = nil
			return
		}
		fmt.Printf("  pod %s/%s: running as %d container(s) at %s\n", p.Namespace, p.Name, len(pr.containers), pr.ip)
	}
	r.report(ctx, p, pr)
}

// start runs the pod's init containers to completion, then its containers, sharing the
// first container's network namespace as a pod's containers do.
func (r *runner) start(ctx context.Context, p *corev1.Pod, pr *podRun) error {
	pdir := filepath.Join(dir, "pods", p.Namespace+"_"+p.Name)
	mounts, err := r.volumes(ctx, p, pdir)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("kc_%s_%s", p.Namespace, p.Name)
	if len(base) > 50 {
		base = base[:50]
	}
	// A pause container holds the pod's network, as the kubelet's sandbox does.
	sandbox := base + "_pod"
	_, _ = cmd("docker", "rm", "-f", sandbox)
	sandboxArgs := append([]string{"run", "-d", "--name", sandbox, "--network", netName, "--hostname", truncate(p.Name, 63),
		"--add-host", "kubernetes.default.svc:" + r.apiHost, "--add-host", "kubernetes.default:" + r.apiHost, "--add-host", "kubernetes:" + r.apiHost,
		"--dns", r.dnsIP, "--dns-search", p.Namespace + ".svc.cluster.local", "--dns-search", "svc.cluster.local", "--dns-search", "cluster.local",
		"--dns-option", "ndots:5"}, podLabels(p.UID)...)
	if _, err := cmd("docker", append(sandboxArgs, "registry.k8s.io/pause:3.10")...); err != nil {
		return err
	}
	// Service IPs go through kube-proxy, as a node's own iptables would send them.
	if out, err := cmd("docker", "run", "--rm", "--network", "container:"+sandbox, "--label", netLabel(), "--cap-add", "NET_ADMIN", toolImage,
		"ip", "route", "add", serviceCIDR, "via", r.proxyIP); err != nil {
		return fmt.Errorf("service route: %v: %s", err, out)
	}
	pr.containers = []string{sandbox}
	ip, err := cmd("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", sandbox)
	if err != nil {
		return err
	}
	pr.ip = strings.TrimSpace(ip)
	run := func(c corev1.Container, init bool) (string, error) {
		name := fmt.Sprintf("%s_%s", base, c.Name)
		_, _ = cmd("docker", "rm", "-f", name)
		args := append([]string{"run", "--name", name, "--network", "container:" + sandbox}, podLabels(p.UID)...)
		if !init {
			args = append(args, "-d")
			if p.Spec.RestartPolicy == corev1.RestartPolicyAlways || p.Spec.RestartPolicy == "" {
				args = append(args, "--restart", "unless-stopped")
			}
		}
		env, err := r.env(ctx, p, c, pr.ip)
		env = append(env, "KUBERNETES_SERVICE_PORT="+r.apiPort, "KUBERNETES_SERVICE_PORT_HTTPS="+r.apiPort)
		if err != nil {
			return "", err
		}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		for _, m := range c.VolumeMounts {
			src, ok := mounts[m.Name]
			if !ok {
				continue
			}
			if m.SubPath != "" {
				src = filepath.Join(src, m.SubPath)
			}
			opt := ""
			if m.ReadOnly {
				opt = ":ro"
			}
			args = append(args, "-v", src+":"+m.MountPath+opt)
		}
		if c.WorkingDir != "" {
			args = append(args, "-w", c.WorkingDir)
		}
		if sc := c.SecurityContext; sc != nil && sc.RunAsUser != nil {
			args = append(args, "--user", fmt.Sprint(*sc.RunAsUser))
		} else if psc := p.Spec.SecurityContext; psc != nil && psc.RunAsUser != nil {
			args = append(args, "--user", fmt.Sprint(*psc.RunAsUser))
		}
		vars := map[string]string{}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			vars[k] = v
		}
		rest := expand(c.Args, vars)
		if len(c.Command) > 0 {
			command := expand(c.Command, vars)
			args = append(args, "--entrypoint", command[0])
			rest = append(append([]string{}, command[1:]...), rest...)
		}
		args = append(args, c.Image)
		args = append(args, rest...)
		out, err := cmd("docker", args...)
		if init && err != nil {
			return name, fmt.Errorf("init container %s: %v: %s", c.Name, err, out)
		}
		return name, err
	}
	for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
		if err := pull(c); err != nil {
			return err
		}
	}
	for _, c := range p.Spec.InitContainers {
		if _, err := run(c, true); err != nil {
			return err
		}
	}
	for _, c := range p.Spec.Containers {
		name, err := run(c, false)
		if err != nil {
			return err
		}
		pr.containers = append(pr.containers, name)
		if pr.byName == nil {
			pr.byName = map[string]string{}
		}
		pr.byName[c.Name] = name
	}
	pr.started = time.Now()
	return nil
}

// volumes writes Secrets, ConfigMaps, projected tokens and the downward API to files, as
// the kubelet does, and makes empty dirs. Files are a snapshot taken at start.
func (r *runner) volumes(ctx context.Context, p *corev1.Pod, pdir string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range p.Spec.Volumes {
		vdir := filepath.Join(pdir, "vol", v.Name)
		_ = os.RemoveAll(vdir)
		if err := os.MkdirAll(vdir, 0o777); err != nil {
			return nil, err
		}
		_ = os.Chmod(vdir, 0o777)
		write := func(name string, data []byte) error {
			f := filepath.Join(vdir, name)
			_ = os.MkdirAll(filepath.Dir(f), 0o777)
			return os.WriteFile(f, data, 0o644)
		}
		switch {
		case v.Secret != nil:
			s, err := r.cs.CoreV1().Secrets(p.Namespace).Get(ctx, v.Secret.SecretName, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) && v.Secret.Optional != nil && *v.Secret.Optional {
					break
				}
				return nil, fmt.Errorf("secret %s: %v", v.Secret.SecretName, err)
			}
			for k, d := range itemsB(s.Data, v.Secret.Items) {
				_ = write(k, d)
			}
		case v.ConfigMap != nil:
			cm, err := r.cs.CoreV1().ConfigMaps(p.Namespace).Get(ctx, v.ConfigMap.Name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) && v.ConfigMap.Optional != nil && *v.ConfigMap.Optional {
					break
				}
				return nil, fmt.Errorf("configmap %s: %v", v.ConfigMap.Name, err)
			}
			for k, d := range itemsCM(cm, v.ConfigMap.Items) {
				_ = write(k, d)
			}
		case v.Projected != nil:
			for _, src := range v.Projected.Sources {
				switch {
				case src.ServiceAccountToken != nil:
					exp := int64(3600)
					if src.ServiceAccountToken.ExpirationSeconds != nil {
						exp = *src.ServiceAccountToken.ExpirationSeconds
					}
					tr := &authv1.TokenRequest{Spec: authv1.TokenRequestSpec{ExpirationSeconds: &exp,
						BoundObjectRef: &authv1.BoundObjectReference{Kind: "Pod", APIVersion: "v1", Name: p.Name, UID: p.UID}}}
					if src.ServiceAccountToken.Audience != "" {
						tr.Spec.Audiences = []string{src.ServiceAccountToken.Audience}
					}
					sa := p.Spec.ServiceAccountName
					if sa == "" {
						sa = "default"
					}
					tok, err := r.cs.CoreV1().ServiceAccounts(p.Namespace).CreateToken(ctx, sa, tr, metav1.CreateOptions{})
					if err != nil {
						return nil, fmt.Errorf("token for %s: %v", sa, err)
					}
					_ = write(src.ServiceAccountToken.Path, []byte(tok.Status.Token))
				case src.ConfigMap != nil:
					cm, err := r.cs.CoreV1().ConfigMaps(p.Namespace).Get(ctx, src.ConfigMap.Name, metav1.GetOptions{})
					if err != nil {
						if apierrors.IsNotFound(err) && src.ConfigMap.Optional != nil && *src.ConfigMap.Optional {
							continue
						}
						return nil, fmt.Errorf("configmap %s: %v", src.ConfigMap.Name, err)
					}
					for k, d := range itemsCM(cm, src.ConfigMap.Items) {
						_ = write(k, d)
					}
				case src.Secret != nil:
					s, err := r.cs.CoreV1().Secrets(p.Namespace).Get(ctx, src.Secret.Name, metav1.GetOptions{})
					if err != nil {
						if apierrors.IsNotFound(err) && src.Secret.Optional != nil && *src.Secret.Optional {
							continue
						}
						return nil, fmt.Errorf("secret %s: %v", src.Secret.Name, err)
					}
					for k, d := range itemsB(s.Data, src.Secret.Items) {
						_ = write(k, d)
					}
				case src.DownwardAPI != nil:
					for _, it := range src.DownwardAPI.Items {
						if it.FieldRef != nil {
							_ = write(it.Path, []byte(fieldRef(p, it.FieldRef.FieldPath, "")))
						}
					}
				}
			}
		case v.DownwardAPI != nil:
			for _, it := range v.DownwardAPI.Items {
				if it.FieldRef != nil {
					_ = write(it.Path, []byte(fieldRef(p, it.FieldRef.FieldPath, "")))
				}
			}
		case v.EmptyDir != nil:
		default:
			continue
		}
		out[v.Name] = vdir
	}
	return out, nil
}

func (r *runner) env(ctx context.Context, p *corev1.Pod, c corev1.Container, ip string) ([]string, error) {
	env := []string{"KUBERNETES_SERVICE_HOST=kubernetes.default.svc"}
	for _, ef := range c.EnvFrom {
		switch {
		case ef.ConfigMapRef != nil:
			cm, err := r.cs.CoreV1().ConfigMaps(p.Namespace).Get(ctx, ef.ConfigMapRef.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			for k, v := range cm.Data {
				env = append(env, ef.Prefix+k+"="+v)
			}
		case ef.SecretRef != nil:
			s, err := r.cs.CoreV1().Secrets(p.Namespace).Get(ctx, ef.SecretRef.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			for k, v := range s.Data {
				env = append(env, ef.Prefix+k+"="+string(v))
			}
		}
	}
	for _, e := range c.Env {
		val := e.Value
		if vf := e.ValueFrom; vf != nil {
			switch {
			case vf.FieldRef != nil:
				val = fieldRef(p, vf.FieldRef.FieldPath, ip)
			case vf.SecretKeyRef != nil:
				s, err := r.cs.CoreV1().Secrets(p.Namespace).Get(ctx, vf.SecretKeyRef.Name, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				val = string(s.Data[vf.SecretKeyRef.Key])
			case vf.ConfigMapKeyRef != nil:
				cm, err := r.cs.CoreV1().ConfigMaps(p.Namespace).Get(ctx, vf.ConfigMapKeyRef.Name, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				val = cm.Data[vf.ConfigMapKeyRef.Key]
			case vf.ResourceFieldRef != nil:
				val = "1"
			}
		}
		env = append(env, e.Name+"="+val)
	}
	return env, nil
}

func fieldRef(p *corev1.Pod, path, ip string) string {
	switch path {
	case "metadata.name":
		return p.Name
	case "metadata.namespace":
		return p.Namespace
	case "metadata.uid":
		return string(p.UID)
	case "spec.nodeName":
		return nodeName
	case "spec.serviceAccountName":
		return p.Spec.ServiceAccountName
	case "status.podIP", "status.hostIP":
		return ip
	}
	return ""
}

// reportPull marks a pod whose image could not be pulled: Pending, not ready, the container
// waiting in ErrImagePull.
func (r *runner) reportPull(ctx context.Context, p *corev1.Pod, pe *pullError) {
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if cs.Name == pe.container && cs.State.Waiting != nil && cs.State.Waiting.Reason == "ErrImagePull" {
			return
		}
	}
	var statuses, inits []corev1.ContainerStatus
	waiting := func(c corev1.Container) corev1.ContainerStatus {
		cs := corev1.ContainerStatus{Name: c.Name, Image: c.Image, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}
		if c.Name == pe.container {
			cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: pe.Error()}
		}
		return cs
	}
	for _, c := range p.Spec.InitContainers {
		inits = append(inits, waiting(c))
	}
	for _, c := range p.Spec.Containers {
		statuses = append(statuses, waiting(c))
	}
	cond := func(t corev1.PodConditionType, s corev1.ConditionStatus) corev1.PodCondition {
		return corev1.PodCondition{Type: t, Status: s, LastTransitionTime: metav1.Now()}
	}
	status := corev1.PodStatus{
		Phase: corev1.PodPending, HostIP: r.kwokIP,
		Conditions: []corev1.PodCondition{cond(corev1.PodScheduled, corev1.ConditionTrue), cond(corev1.PodInitialized, corev1.ConditionFalse),
			cond(corev1.ContainersReady, corev1.ConditionFalse), cond(corev1.PodReady, corev1.ConditionFalse)},
		InitContainerStatuses: inits, ContainerStatuses: statuses,
	}
	b, _ := json.Marshal(map[string]any{"status": status})
	if _, err := r.cs.CoreV1().Pods(p.Namespace).Patch(ctx, p.Name, types.MergePatchType, b, metav1.PatchOptions{}, "status"); err == nil {
		fmt.Printf("  pod %s/%s: ErrImagePull %s\n", p.Namespace, p.Name, pe.image)
	}
}

// report writes the pod's status from its containers, running readiness probes the way
// the kubelet would, from the cluster's network.
func (r *runner) report(ctx context.Context, p *corev1.Pod, pr *podRun) {
	var statuses []corev1.ContainerStatus
	allReady, anyRunning, allDone, failed := true, false, true, false
	for i, c := range p.Spec.Containers {
		name := pr.containers[i+1]
		out, err := cmd("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}} {{.RestartCount}}", name)
		f := strings.Fields(out)
		cs := corev1.ContainerStatus{Name: c.Name, Image: c.Image, ImageID: c.Image, ContainerID: "docker://" + name}
		if err != nil || len(f) < 3 {
			allReady = false
			statuses = append(statuses, cs)
			continue
		}
		fmt.Sscan(f[2], &cs.RestartCount)
		switch f[0] {
		case "running":
			anyRunning, allDone = true, false
			started := true
			cs.Started = &started
			cs.State.Running = &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(pr.started)}
			cs.Ready = r.probe(c, name, pr.ip)
		case "exited":
			code := 0
			fmt.Sscan(f[1], &code)
			if code != 0 {
				failed = true
			}
			cs.State.Terminated = &corev1.ContainerStateTerminated{ExitCode: int32(code), Reason: map[bool]string{true: "Completed", false: "Error"}[code == 0]}
		default:
			allDone = false
			cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: f[0]}
		}
		if !cs.Ready {
			allReady = false
		}
		statuses = append(statuses, cs)
	}
	phase := corev1.PodPending
	switch {
	case allDone && p.Spec.RestartPolicy != corev1.RestartPolicyAlways && len(statuses) > 0:
		phase = map[bool]corev1.PodPhase{true: corev1.PodFailed, false: corev1.PodSucceeded}[failed]
		allReady = false
	case anyRunning:
		phase = corev1.PodRunning
	}
	cond := func(t corev1.PodConditionType, ok bool) corev1.PodCondition {
		s := corev1.ConditionFalse
		if ok {
			s = corev1.ConditionTrue
		}
		return corev1.PodCondition{Type: t, Status: s, LastTransitionTime: metav1.Now()}
	}
	st := metav1.NewTime(pr.started)
	status := corev1.PodStatus{
		Phase: phase, PodIP: pr.ip, PodIPs: []corev1.PodIP{{IP: pr.ip}}, HostIP: r.kwokIP, StartTime: &st,
		Conditions: []corev1.PodCondition{cond(corev1.PodScheduled, true), cond(corev1.PodInitialized, true),
			cond(corev1.ContainersReady, allReady), cond(corev1.PodReady, allReady)},
		ContainerStatuses: statuses,
	}
	old := p.Status
	if old.Phase == status.Phase && old.PodIP == status.PodIP && readyOf(old) == allReady {
		return
	}
	b, _ := json.Marshal(map[string]any{"status": status})
	if _, err := r.cs.CoreV1().Pods(p.Namespace).Patch(ctx, p.Name, types.MergePatchType, b, metav1.PatchOptions{}, "status"); err == nil {
		fmt.Printf("  pod %s/%s: %s, ready %v\n", p.Namespace, p.Name, phase, allReady)
	}
}

func (r *runner) probe(c corev1.Container, name, ip string) bool {
	pr := c.ReadinessProbe
	if pr != nil && pr.Exec != nil && len(pr.Exec.Command) > 0 {
		_, err := cmd("docker", append([]string{"exec", name}, pr.Exec.Command...)...)
		return err == nil
	}
	if pr == nil || pr.HTTPGet == nil {
		return true
	}
	port := pr.HTTPGet.Port.String()
	if pr.HTTPGet.Port.Type != 0 {
		for _, cp := range c.Ports {
			if cp.Name == pr.HTTPGet.Port.StrVal {
				port = fmt.Sprint(cp.ContainerPort)
			}
		}
	}
	scheme := strings.ToLower(string(pr.HTTPGet.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	_, err := cmd("docker", "exec", netName+"-prober", "curl", "-skf", "-m", "2", fmt.Sprintf("%s://%s:%s%s", scheme, ip, port, pr.HTTPGet.Path))
	return err == nil
}

func readyOf(s corev1.PodStatus) bool {
	for _, c := range s.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podLabels marks a pod's containers: stop removes them by pod, and down by node.
func podLabels(uid types.UID) []string {
	return []string{"--label", "kc.pod=" + string(uid), "--label", netLabel()}
}

// netLabel marks a container as this node's. A pod's app containers share the sandbox's
// network namespace and are on no Docker network, so down finds them by label.
func netLabel() string { return "kc.net=" + netName }

func stop(pr *podRun) {
	out, _ := cmd("docker", "ps", "-aq", "--filter", "label=kc.pod="+string(pr.pod.UID))
	if ids := strings.Fields(out); len(ids) > 0 {
		_, _ = cmd("docker", append([]string{"rm", "-f"}, ids...)...)
	}
}

func (r *runner) summary() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pr := range r.pods {
		fmt.Printf("  real: %s/%s at %s (%d container(s))\n", pr.pod.Namespace, pr.pod.Name, pr.ip, len(pr.containers)-1)
	}
}

func (r *runner) dump() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pr := range r.pods {
		for _, c := range pr.containers[min(1, len(pr.containers)):] {
			out, _ := exec.Command("docker", "logs", "--tail", "8", c).CombinedOutput()
			fmt.Printf("--- %s\n%s", c, out)
		}
	}
}

// ---- checks ----

func issued(kc string) error {
	y := `apiVersion: cert-manager.io/v1
kind: Issuer
metadata: {name: self, namespace: default}
spec: {selfSigned: {}}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: demo, namespace: default}
spec:
  secretName: demo-tls
  dnsNames: [demo.example.com]
  issuerRef: {name: self, kind: Issuer}
`
	if out, err := kubectlIn(kc, y, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return poll(90*time.Second, func() error {
		out, err := cmd("kubectl", "--kubeconfig", kc, "-n", "default", "get", "secret", "demo-tls", "-o", "jsonpath={.data.tls\\.crt}")
		if err != nil || len(strings.TrimSpace(out)) == 0 {
			st, _ := cmd("kubectl", "--kubeconfig", kc, "-n", "default", "get", "certificate", "demo", "-o", "jsonpath={.status.conditions}")
			return fmt.Errorf("no tls.crt yet; certificate conditions %s", st)
		}
		return nil
	})
}

func refused(kc string) error {
	y := `apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: bad, namespace: default}
spec:
  secretName: bad-tls
  issuerRef: {name: self, kind: Issuer}
`
	out, err := kubectlIn(kc, y, "apply", "-f", "-")
	if err == nil {
		return fmt.Errorf("accepted a Certificate with no names: %s", out)
	}
	if !strings.Contains(out, "admission webhook") {
		return fmt.Errorf("refused, but not by the webhook: %s", out)
	}
	fmt.Printf("     refusal: %s\n", strings.TrimSpace(firstLine(out)))
	return nil
}

func proxied(cs *kubernetes.Clientset, kc string) error {
	pods, err := cs.CoreV1().Pods("cert-manager").List(context.Background(), metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=webhook"})
	if err != nil || len(pods.Items) == 0 {
		return fmt.Errorf("no webhook pod: %v", err)
	}
	path := fmt.Sprintf("/api/v1/namespaces/cert-manager/pods/%s:6080/proxy/healthz", pods.Items[0].Name)
	out, err := cmd("kubectl", "--kubeconfig", kc, "get", "--raw", path)
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	fmt.Printf("     %s -> %q\n", path, strings.TrimSpace(out))
	return nil
}

func simulated(kc string) error {
	y := `apiVersion: apps/v1
kind: Deployment
metadata: {name: sim, namespace: default}
spec:
  replicas: 2
  selector: {matchLabels: {app: sim}}
  template:
    metadata: {labels: {app: sim}}
    spec: {containers: [{name: c, image: does-not-exist.invalid/never:1}]}
`
	if out, err := kubectlIn(kc, strings.Replace(y, "does-not-exist.invalid/never:1", "nginx:1.27", 1), "apply", "-f", "-"); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return poll(60*time.Second, func() error {
		out, _ := cmd("kubectl", "--kubeconfig", kc, "-n", "default", "get", "pods", "-l", "app=sim", "-o", "jsonpath={range .items[*]}{.spec.nodeName} {.status.phase}{\"\\n\"}{end}")
		lines := strings.Fields(strings.ReplaceAll(out, "\n", " "))
		if len(lines) != 4 {
			return fmt.Errorf("pods: %q", out)
		}
		for i := 0; i < len(lines); i += 2 {
			if lines[i] == nodeName || lines[i+1] != "Running" {
				return fmt.Errorf("pods: %q", out)
			}
		}
		return nil
	})
}

// ---- helpers ----

func itemsB(data map[string][]byte, items []corev1.KeyToPath) map[string][]byte {
	if len(items) == 0 {
		return data
	}
	out := map[string][]byte{}
	for _, it := range items {
		out[it.Path] = data[it.Key]
	}
	return out
}

func itemsS(data map[string]string, items []corev1.KeyToPath) map[string][]byte {
	out := map[string][]byte{}
	if len(items) == 0 {
		for k, v := range data {
			out[k] = []byte(v)
		}
		return out
	}
	for _, it := range items {
		out[it.Path] = []byte(data[it.Key])
	}
	return out
}

func kubectlIn(kc, stdin string, args ...string) (string, error) {
	c := exec.Command("kubectl", append([]string{"--kubeconfig", kc}, args...)...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	return string(out), err
}

func cmd(name string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	if err != nil {
		return out.String() + errb.String(), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args[:min(len(args), 3)], " "), err, strings.TrimSpace(lastLines(errb.String(), 4)))
	}
	return out.String(), nil
}

func poll(d time.Duration, f func() error) error {
	end := time.Now().Add(d)
	var last error
	for time.Now().Before(end) {
		if last = f(); last == nil {
			return nil
		} else if last == errStop {
			return last
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("not within %s: %v", d, last)
}

func step(t time.Time, format string, a ...any) {
	fmt.Printf("%s (%s)\n", fmt.Sprintf(format, a...), time.Since(t).Round(time.Second))
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	if !*keep {
		down()
	}
	os.Exit(1)
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func mustQ(s string) resource.Quantity { return resource.MustParse(s) }

// expand replaces $(VAR) with the container's env, as the kubelet does; $$ escapes.
func expand(in []string, vars map[string]string) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = expandRe.ReplaceAllStringFunc(a, func(m string) string {
			if m == "$$" {
				return "$"
			}
			if v, ok := vars[m[2:len(m)-1]]; ok {
				return v
			}
			return m
		})
	}
	return out
}

var expandRe = regexp.MustCompile(`\$\$|\$\([A-Za-z_][A-Za-z0-9_]*\)`)

// network starts the cluster's DNS and Service proxy as containers on its network: CoreDNS
// with the kubernetes plugin, and kube-proxy in iptables mode, masquerading so replies
// come back through it.
func network(server string) (string, string, error) {
	if _, err := cmd("docker", "run", "-d", "--name", netName+"-prober", "--network", netName, "--entrypoint", "sleep", proberImage, "infinity"); err != nil {
		return "", "", err
	}
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: kwok, cluster: {server: "%s", insecure-skip-tls-verify: true}}]
users: [{name: admin, user: {client-certificate: /vd/admin.crt, client-key: /vd/admin.key}}]
contexts: [{name: kwok, context: {cluster: kwok, user: admin}}]
current-context: kwok
`, server)
	corefile := `.:53 {
    errors
    kubernetes cluster.local in-addr.arpa ip6.arpa {
        kubeconfig /vd/net.kubeconfig kwok
        pods insecure
        fallthrough in-addr.arpa ip6.arpa
    }
    forward . /etc/resolv.conf
    cache 5
}
`
	if err := os.WriteFile(filepath.Join(dir, "net.kubeconfig"), []byte(kc), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "Corefile"), []byte(corefile), 0o644); err != nil {
		return "", "", err
	}
	// As root: the admin key is 0600 and owned by whoever runs this, and the image's own user
	// is neither.
	if _, err := cmd("docker", "run", "-d", "--name", netName+"-dns", "--user", "0:0", "--network", netName, "-v", dir+":/vd:ro", "--add-host", "host.docker.internal:host-gateway",
		dnsImage, "-conf", "/vd/Corefile"); err != nil {
		return "", "", err
	}
	if _, err := cmd("docker", "run", "-d", "--name", netName+"-proxy", "--network", netName, "--privileged", "--add-host", "host.docker.internal:host-gateway",
		"--sysctl", "net.ipv4.ip_forward=1", "-v", dir+":/vd:ro", "--entrypoint", "kube-proxy", proxyImage,
		"--kubeconfig=/vd/net.kubeconfig", "--hostname-override="+nodeName, "--proxy-mode=iptables",
		"--masquerade-all", "--conntrack-max-per-core=0", "--cluster-cidr="+serviceCIDR); err != nil {
		return "", "", err
	}
	ipOf := func(name string) (string, error) {
		out, err := cmd("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
		ip := strings.TrimSpace(out)
		if err == nil && net.ParseIP(ip) == nil {
			// Docker 29 prints "invalid IP" for a container with no address, one that has exited.
			return "", fmt.Errorf("no address: %s", containerState(name))
		}
		return ip, err
	}
	dnsIP, err := ipOf(netName + "-dns")
	if err != nil {
		return "", "", err
	}
	proxyIP, err := ipOf(netName + "-proxy")
	if err != nil {
		return "", "", err
	}
	// Ready when CoreDNS answers for the kubernetes Service and kube-proxy has synced.
	err = poll(60*time.Second, func() error {
		if out, err := cmd("sh", "-c", "docker logs "+netName+"-proxy 2>&1"); err != nil || !strings.Contains(out, "Caches are synced") {
			return fmt.Errorf("kube-proxy not synced: %s", lastLines(out, 3))
		}
		out, err := cmd("docker", "run", "--rm", "--network", netName, toolImage, "nslookup", "kubernetes.default.svc.cluster.local", dnsIP)
		if err != nil || strings.Contains(out, "can't find") || strings.Count(out, "Address") < 2 {
			return fmt.Errorf("dns: %v %s", err, out)
		}
		return nil
	})
	if err != nil {
		err = fmt.Errorf("%v; %s; %s", err, containerState(netName+"-dns"), containerState(netName+"-proxy"))
	}
	return dnsIP, proxyIP, err
}

// containerState says how a container is doing and ends with its last log lines, for an error
// raised before cleanup removes it.
func containerState(name string) string {
	state, _ := cmd("docker", "inspect", "-f", "{{.State.Status}} exit={{.State.ExitCode}} {{.State.Error}}", name)
	logs, _ := cmd("sh", "-c", "docker logs --tail 5 "+name+" 2>&1")
	return fmt.Sprintf("%s %s, logs: %s", name, strings.TrimSpace(state), strings.Join(strings.Fields(lastLines(logs, 5)), " "))
}

func services(cs *kubernetes.Clientset, kc string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "net", Annotations: map[string]string{
		"scheduler.alpha.kubernetes.io/node-selector":      "role=containers",
		"scheduler.alpha.kubernetes.io/defaultTolerations": `[{"key":"` + taintKey + `","operator":"Exists","effect":"NoSchedule"}]`,
	}}}
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{}); err != nil {
		return err
	}
	y := `apiVersion: apps/v1
kind: Deployment
metadata: {name: echo, namespace: net}
spec:
  selector: {matchLabels: {app: echo}}
  template:
    metadata: {labels: {app: echo}}
    spec:
      containers:
      - name: echo
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        args: [netexec, --http-port=8080]
        readinessProbe: {httpGet: {path: /hostname, port: 8080}}
---
apiVersion: v1
kind: Service
metadata: {name: echo, namespace: net}
spec:
  selector: {app: echo}
  ports: [{port: 80, targetPort: 8080}]
---
apiVersion: batch/v1
kind: Job
metadata: {name: client, namespace: net}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: client
        image: ` + proberImage + `
        command: [sh, -c]
        args:
        - |
          set -e
          for i in $(seq 1 30); do curl -sf -m 2 http://echo/hostname >/dev/null && break; sleep 1; done
          echo "short name:  $(curl -sf -m 3 http://echo/hostname)"
          echo "full name:   $(curl -sf -m 3 http://echo.net.svc.cluster.local/hostname)"
          code=$(curl -sk -m 3 -o /dev/null -w '%{http_code}' https://cert-manager-webhook.cert-manager.svc/)
          echo "cert-manager webhook, across namespaces: HTTP $code"
          test "$code" != 000
          code=$(curl -sk -m 3 -o /dev/null -w '%{http_code}' https://kubernetes.default.svc.cluster.local/healthz)
          echo "kubernetes Service through its ClusterIP: HTTP $code"
          test "$code" != 000
`
	if out, err := kubectlIn(kc, y, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	err := poll(120*time.Second, func() error {
		out, _ := cmd("kubectl", "--kubeconfig", kc, "-n", "net", "get", "job", "client", "-o", "jsonpath={.status.succeeded}/{.status.failed}")
		switch strings.TrimSpace(out) {
		case "1/":
			return nil
		case "/1":
			return errStop
		}
		return fmt.Errorf("job %s", out)
	})
	logs, _ := cmd("sh", "-c", "docker logs $(docker ps -aq --filter name=kc_net_client | head -1) 2>&1")
	for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
		fmt.Printf("     %s\n", l)
	}
	if err != nil {
		return fmt.Errorf("client job did not succeed: %v", err)
	}
	return nil
}

var errStop = fmt.Errorf("failed")

// itemsCM is a ConfigMap's data and binary data, as the kubelet writes both.
func itemsCM(cm *corev1.ConfigMap, items []corev1.KeyToPath) map[string][]byte {
	all := map[string][]byte{}
	for k, v := range cm.Data {
		all[k] = []byte(v)
	}
	for k, v := range cm.BinaryData {
		all[k] = v
	}
	return itemsB(all, items)
}
