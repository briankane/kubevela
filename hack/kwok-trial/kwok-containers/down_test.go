package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

// TestDownRemovesPodContainers runs against this host's Docker: a pod's app containers
// share the sandbox's network namespace, so they are on no Docker network of their own,
// and down must still remove them, as it does when the node starts after a hard kill.
func TestDownRemovesPodContainers(t *testing.T) {
	if _, err := cmd("docker", "info"); err != nil {
		t.Skipf("no Docker: %v", err)
	}
	old := netName
	netName = fmt.Sprintf("kctest-%d", os.Getpid())
	defer func() { netName = old }()
	uid := types.UID("uid-" + netName)
	sandbox, app := netName+"_pod", netName+"_app"
	t.Cleanup(func() { _, _ = cmd("docker", "rm", "-f", sandbox, app); _, _ = cmd("docker", "network", "rm", netName) })

	if _, err := cmd("docker", "network", "create", netName); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd("docker", append(append([]string{"run", "-d", "--name", sandbox, "--network", netName}, podLabels(uid)...), "registry.k8s.io/pause:3.10")...); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd("docker", append(append([]string{"run", "-d", "--name", app, "--network", "container:" + sandbox}, podLabels(uid)...), "registry.k8s.io/pause:3.10")...); err != nil {
		t.Fatal(err)
	}

	down()

	out, err := cmd("docker", "ps", "-a", "--format", "{{.Names}}", "--filter", "name="+netName)
	if err != nil {
		t.Fatal(err)
	}
	if left := strings.Fields(out); len(left) > 0 {
		t.Fatalf("down left %v", left)
	}
	if _, err := cmd("docker", "network", "inspect", netName); err == nil {
		t.Fatalf("down left the network %s", netName)
	}
}
