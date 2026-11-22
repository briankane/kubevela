package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"k8s.io/cri-streaming/pkg/streaming/portforward"
	"k8s.io/cri-streaming/pkg/streaming/remotecommand"
)

// serveKubelet serves the part of the kubelet API the API server proxies to: container
// logs, exec and port-forward, backed by docker logs, docker exec, and a netcat container
// in the pod's network namespace. The streaming protocols are the kubelet's own.
func (r *runner) serveKubelet(ctx context.Context, port int) error {
	_, certPEM, keyPEM, err := selfSigned("127.0.0.1")
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/containerLogs/{ns}/{pod}/{container}", r.logs)
	mux.HandleFunc("/exec/{ns}/{pod}/{container}", r.exec)
	mux.HandleFunc("/portForward/{ns}/{pod}", r.portForward)
	for _, addr := range kubeletAddrs() {
		ln, err := tls.Listen("tcp", fmt.Sprintf("%s:%d", addr, port), &tls.Config{Certificates: []tls.Certificate{pair}})
		if err != nil {
			return err
		}
		go func() { _ = http.Serve(ln, mux) }()
		go func() { <-ctx.Done(); _ = ln.Close() }()
	}
	return nil
}

// kubeletAddrs are where the kubelet API listens: loopback for the API server, and on Linux
// Docker's host gateway too, since the proxy reaches the node address through it and Docker
// Engine, unlike Docker Desktop, does not forward the gateway to the host's loopback.
func kubeletAddrs() []string {
	addrs := []string{"127.0.0.1"}
	if runtime.GOOS == "linux" {
		gw, err := cmd("docker", "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}")
		if gw = strings.TrimSpace(gw); err == nil && net.ParseIP(gw) != nil {
			addrs = append(addrs, gw)
		}
	}
	return addrs
}

// lookup finds a running pod's sandbox and the Docker name of one of its containers.
func (r *runner) lookup(ns, pod, container string) (sandbox, name string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pr := range r.pods {
		if pr.pod.Namespace != ns || pr.pod.Name != pod || len(pr.containers) == 0 {
			continue
		}
		if container == "" {
			return pr.containers[0], "", true
		}
		n, found := pr.byName[container]
		return pr.containers[0], n, found
	}
	return "", "", false
}

func (r *runner) logs(w http.ResponseWriter, req *http.Request) {
	_, name, ok := r.lookup(req.PathValue("ns"), req.PathValue("pod"), req.PathValue("container"))
	if !ok {
		http.Error(w, "no such container on this node", http.StatusNotFound)
		return
	}
	q := req.URL.Query()
	args := []string{"logs"}
	if q.Get("follow") == "true" {
		args = append(args, "--follow")
	}
	if n := q.Get("tailLines"); n != "" {
		args = append(args, "--tail", n)
	}
	if q.Get("timestamps") == "true" {
		args = append(args, "--timestamps")
	}
	if s := q.Get("sinceSeconds"); s != "" {
		args = append(args, "--since", s+"s")
	}
	c := exec.CommandContext(req.Context(), "docker", append(args, name)...)
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() { _ = c.Wait(); _ = pw.Close() }()
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	sc := bufio.NewReader(pr)
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				_ = c.Process.Kill()
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *runner) exec(w http.ResponseWriter, req *http.Request) {
	ns, pod, container := req.PathValue("ns"), req.PathValue("pod"), req.PathValue("container")
	if _, _, ok := r.lookup(ns, pod, container); !ok {
		http.Error(w, "no such container on this node", http.StatusNotFound)
		return
	}
	opts, err := remotecommand.NewOptions(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	remotecommand.ServeExec(w, req, &dockerExec{r: r, ns: ns}, pod, "", container, req.URL.Query()["command"], opts,
		4*time.Hour, 30*time.Second, remotecommand.SupportedStreamingProtocols)
}

type dockerExec struct {
	r  *runner
	ns string
}

func (d *dockerExec) ExecInContainer(ctx context.Context, pod, _ string, container string, cmd []string, in io.Reader,
	out, errw io.WriteCloser, tty bool, _ <-chan remotecommand.TerminalSize, _ time.Duration) error {
	_, name, ok := d.r.lookup(d.ns, pod, container)
	if !ok {
		return fmt.Errorf("container %s of pod %s/%s is not running", container, d.ns, pod)
	}
	args := []string{"exec"}
	if in != nil {
		args = append(args, "-i")
	}
	if tty {
		args = append(args, "-t")
	}
	c := exec.CommandContext(ctx, "docker", append(append(args, name), cmd...)...)
	c.Stdin = in
	if out != nil {
		c.Stdout = out
	}
	if errw != nil {
		c.Stderr = errw
	} else if out != nil {
		c.Stderr = out
	}
	return c.Run()
}

func (r *runner) portForward(w http.ResponseWriter, req *http.Request) {
	ns, pod := req.PathValue("ns"), req.PathValue("pod")
	if _, _, ok := r.lookup(ns, pod, ""); !ok {
		http.Error(w, "no such pod on this node", http.StatusNotFound)
		return
	}
	opts, err := portforward.NewV4Options(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	portforward.ServePortForward(w, req, &dockerForward{r: r, ns: ns}, pod, "", opts, 4*time.Hour, 30*time.Second, portforward.SupportedProtocols)
}

type dockerForward struct {
	r  *runner
	ns string
}

// PortForward connects the stream to the port inside the pod's network namespace, as the
// kubelet does, through a netcat container that joins it.
func (d *dockerForward) PortForward(ctx context.Context, pod, _ string, port int32, stream io.ReadWriteCloser) error {
	sandbox, _, ok := d.r.lookup(d.ns, pod, "")
	if !ok {
		return fmt.Errorf("pod %s/%s is not running", d.ns, pod)
	}
	c := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--network", "container:"+sandbox, "--label", netLabel(), toolImage,
		"nc", "127.0.0.1", strconv.Itoa(int(port)))
	c.Stdin, c.Stdout = stream, stream
	defer stream.Close()
	return c.Run()
}
