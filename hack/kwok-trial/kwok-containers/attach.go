package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	admregv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// attachMain adds a container node to a KWOK cluster that is already running on this host
// (kwokctl's binary runtime): pods in the named namespaces run for real in Docker, with
// CoreDNS and kube-proxy, and an HTTP proxy published on the host lets host processes
// reach Service names. It runs until interrupted, then removes what it added.
var egress bool

func attachMain(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	kubeconfig := fs.String("kubeconfig", "", "the cluster's admin kubeconfig (required)")
	name := fs.String("name", "kc", "Docker network and container prefix")
	namespaces := fs.String("namespaces", "", "comma-separated namespaces whose pods run for real")
	podSelector := fs.String("pod-selector", "", "a label selector, or several separated by \";\" and OR'd: pods matching run for real, in any namespace")
	kubeletPort := fs.Int("kubelet-port", 0, "host port for the node's kubelet API (logs, exec, port-forward); 0: none")
	cidr := fs.String("service-cidr", "", "the cluster's Service range (required)")
	webhookPort := fs.Int("webhook-port", 9871, "host port for the scheduling webhook")
	proxyPort := fs.Int("proxy-port", 18888, "host port for the HTTP proxy into the cluster network")
	egressSocket := fs.String("egress-socket", "", "a Unix socket forwarded to the proxy, for an API server egress selector (HTTPConnect over uds): the proxy then sends \"localhost\", the node's kubelet address, to this host")
	_ = fs.Parse(args)
	if *kubeconfig == "" || *cidr == "" || (*namespaces == "") == (*podSelector == "") {
		fmt.Fprintln(os.Stderr, "error: -kubeconfig, -service-cidr, and one of -namespaces or -pod-selector are required")
		fs.Usage()
		os.Exit(2)
	}
	netName, serviceCIDR = *name, *cidr
	dir = filepath.Join(os.TempDir(), "kwok-containers-"+*name)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		fail("kubeconfig: %v", err)
	}
	cfg.QPS, cfg.Burst = 100, 200
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fail("client: %v", err)
	}
	u, err := url.Parse(cfg.Host)
	if err != nil {
		fail("server: %v", err)
	}
	apiPort := u.Port()

	down()
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	for f, data := range map[string][]byte{"admin.crt": orFile(cfg.CertData, cfg.CertFile), "admin.key": orFile(cfg.KeyData, cfg.KeyFile)} {
		if len(data) == 0 {
			fail("the kubeconfig has no client certificate (%s)", f)
		}
		_ = os.WriteFile(filepath.Join(dir, f), data, 0o600)
	}
	if _, err := cmd("docker", "network", "create", netName); err != nil {
		fail("network: %v", err)
	}
	t := time.Now()
	dnsIP, proxyIP, err := network("https://host.docker.internal:" + apiPort)
	if err != nil {
		cleanup(cs, *name)
		fail("dns and services: %v", err)
	}
	step(t, "CoreDNS at %s, kube-proxy at %s", dnsIP, proxyIP)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{"role": "containers", "kubernetes.io/hostname": nodeName, "kubernetes.io/os": "linux"}},
		Spec:       corev1.NodeSpec{Taints: []corev1.Taint{{Key: taintKey, Value: "true", Effect: corev1.TaintEffectNoSchedule}}},
	}
	_ = cs.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	if _, err := cs.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		cleanup(cs, *name)
		fail("node: %v", err)
	}
	r := &runner{cs: cs, kwokIP: dnsIP, apiHost: "host-gateway", apiPort: apiPort, dnsIP: dnsIP, proxyIP: proxyIP, nodeIP: proxyIP, pods: map[types.UID]*podRun{}}
	if *kubeletPort != 0 {
		r.nodeAddr, r.kubeletPort = "localhost", int32(*kubeletPort)
		if err := r.serveKubelet(ctx, *kubeletPort); err != nil {
			cleanup(cs, *name)
			fail("kubelet API: %v", err)
		}
	}
	go r.nodeHeartbeat(ctx)
	go r.watch(ctx)

	if err := serveScheduling(ctx, cs, *name, *webhookPort, *namespaces, *podSelector); err != nil {
		cleanup(cs, *name)
		fail("webhook: %v", err)
	}
	egress = *egressSocket != ""
	if err := httpProxy(dnsIP, proxyIP, *proxyPort); err != nil {
		cleanup(cs, *name)
		fail("http proxy: %v", err)
	}
	if egress {
		if err := forwardSocket(ctx, *egressSocket, fmt.Sprintf("127.0.0.1:%d", *proxyPort)); err != nil {
			cleanup(cs, *name)
			fail("egress socket: %v", err)
		}
	}
	fmt.Printf("ready: pods in %s%s run for real on node %q (kubelet API port %d); host processes reach Services with HTTP_PROXY=http://127.0.0.1:%d\n",
		*namespaces, *podSelector, nodeName, *kubeletPort, *proxyPort)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	cleanup(cs, *name)
	fmt.Println("removed: webhook, node, containers")
}

func cleanup(cs *kubernetes.Clientset, name string) {
	ctx := context.Background()
	_ = cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(ctx, "kwok-containers-"+name, metav1.DeleteOptions{})
	_ = cs.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	down()
}

func orFile(data []byte, file string) []byte {
	if len(data) > 0 || file == "" {
		return data
	}
	b, _ := os.ReadFile(file)
	return b
}

// serveScheduling serves a mutating webhook that puts pods in the named namespaces on the
// container node, and registers it. Built-in admission plugins would need the namespace
// annotated before its first pod, which a test creating its own namespace never does.
func serveScheduling(ctx context.Context, cs *kubernetes.Clientset, name string, port int, namespaces, podSelector string) error {
	caPEM, certPEM, keyPEM, err := selfSigned("127.0.0.1")
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	ln, err := tls.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/schedule", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var review admissionv1.AdmissionReview
		if err := json.Unmarshal(body, &review); err != nil || review.Request == nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		var pod corev1.Pod
		_ = json.Unmarshal(review.Request.Object.Raw, &pod)
		var patch []map[string]any
		if pod.Spec.NodeSelector == nil {
			patch = append(patch, map[string]any{"op": "add", "path": "/spec/nodeSelector", "value": map[string]string{"role": "containers"}})
		} else {
			patch = append(patch, map[string]any{"op": "add", "path": "/spec/nodeSelector/role", "value": "containers"})
		}
		tol := corev1.Toleration{Key: taintKey, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
		if pod.Spec.Tolerations == nil {
			patch = append(patch, map[string]any{"op": "add", "path": "/spec/tolerations", "value": []corev1.Toleration{tol}})
		} else {
			patch = append(patch, map[string]any{"op": "add", "path": "/spec/tolerations/-", "value": tol})
		}
		b, _ := json.Marshal(patch)
		pt := admissionv1.PatchTypeJSONPatch
		review.Response = &admissionv1.AdmissionResponse{UID: review.Request.UID, Allowed: true, Patch: b, PatchType: &pt}
		review.Request = nil
		_ = json.NewEncoder(w).Encode(review)
	})
	go func() { _ = http.Serve(ln, mux) }()
	go func() { <-ctx.Done(); _ = ln.Close() }()

	fail := admregv1.Fail
	none := admregv1.SideEffectClassNone
	path := "/schedule"
	u := fmt.Sprintf("https://127.0.0.1:%d%s", port, path)
	hook := &admregv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "kwok-containers-" + name},
		Webhooks: []admregv1.MutatingWebhook{{
			Name:                    "schedule.kwok-containers.vela.test",
			ClientConfig:            admregv1.WebhookClientConfig{URL: &u, CABundle: caPEM},
			Rules:                   []admregv1.RuleWithOperations{{Operations: []admregv1.OperationType{admregv1.Create}, Rule: admregv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}},
			FailurePolicy:           &fail,
			SideEffects:             &none,
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
	if namespaces != "" {
		hook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: strings.Split(namespaces, ",")}}}
	}
	if podSelector != "" {
		// Selectors separated by ";" are OR'd: one webhook each, since a label selector
		// can only AND its terms.
		base := hook.Webhooks[0]
		hook.Webhooks = nil
		for i, part := range strings.Split(podSelector, ";") {
			sel, err := metav1.ParseToLabelSelector(strings.TrimSpace(part))
			if err != nil {
				return err
			}
			w := base
			w.Name = fmt.Sprintf("schedule-%d.kwok-containers.vela.test", i)
			w.ObjectSelector = sel
			hook.Webhooks = append(hook.Webhooks, w)
		}
	}
	_ = cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(ctx, hook.Name, metav1.DeleteOptions{})
	_, err = cs.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, hook, metav1.CreateOptions{})
	return err
}

// httpProxy runs squid on the cluster network, resolving through CoreDNS and routing
// Service IPs through kube-proxy like a real pod, and publishes it on the host. Squid
// keeps client connections alive between requests, which a Go client reusing one for an
// upload (an OCI blob PUT) depends on.
//
// For an API server egress the kubelet API, which listens on this host's loopback under
// the node address "localhost", is reached through Docker's host gateway.
func httpProxy(dnsIP, proxyIP string, port int) error {
	hosts := ""
	if egress {
		hosts = `getent ahostsv4 host.docker.internal | awk 'NR==1{print $1" localhost"}' > /tmp/hosts
`
	}
	conf := strings.Join([]string{
		"http_port 8888", "http_access allow all", "cache deny all", "via off", "forwarded_for delete",
		"dns_nameservers " + dnsIP, "dns_defnames on", "pid_filename none", "shutdown_lifetime 1 seconds",
		"access_log none", "cache_log stdio:/dev/stderr", "client_request_buffer_max_size 1 GB",
	}, `\n`)
	if egress {
		conf += `\nhosts_file /tmp/hosts`
	}
	script := fmt.Sprintf(`set -e
apk add -q --no-cache squid
ip route add %s via %s
%sprintf '%s\n' > /tmp/squid.conf
exec squid -N -f /tmp/squid.conf`, serviceCIDR, proxyIP, hosts, conf)
	if out, err := cmd("docker", "run", "-d", "--name", netName+"-http", "--network", netName, "--cap-add", "NET_ADMIN", "--add-host", "host.docker.internal:host-gateway",
		"--dns", dnsIP, "--dns-search", "svc.cluster.local", "--dns-search", "cluster.local", "--dns-option", "ndots:5",
		"-p", fmt.Sprintf("127.0.0.1:%d:8888", port), "alpine:3.20", "sh", "-c", script); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return poll(90*time.Second, func() error {
		out, _ := cmd("sh", "-c", "docker logs "+netName+"-http 2>&1")
		if !strings.Contains(out, "Accepting HTTP Socket connections") {
			return fmt.Errorf("squid not listening: %s", lastLines(out, 3))
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = c.Close()
		}
		return err
	})
}

func selfSigned(ip string) (caPEM, certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: ip},
		IPAddresses: []net.IP{net.ParseIP(ip)}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, certPEM, keyPEM, nil
}

// forwardSocket accepts on a Unix socket and copies each connection to addr.
func forwardSocket(ctx context.Context, path, addr string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close(); _ = os.Remove(path) }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", addr)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return nil
}
