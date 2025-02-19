package configprocessor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/klog"

	"cuelang.org/go/cue"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/pkg/errors"

	"github.com/oam-dev/kubevela/pkg/builtin"
	"github.com/oam-dev/kubevela/pkg/builtin/registry"
)

func init() {
	registry.RegisterRunner("config", newCmd)
}

type Cmd struct{}

func (c Cmd) Run(meta *registry.Meta) (results interface{}, err error) {
	var name string
	var namespace string
	nameObj := meta.Obj.LookupPath(value.FieldPath("name"))
	if !nameObj.Exists() {
		return nil, errors.New("config specifies no name")
	} else {
		name, err = nameObj.String()
	}

	namespaceObj := meta.Obj.LookupPath(value.FieldPath("namespace"))
	if !namespaceObj.Exists() {
		return nil, errors.New("config specifies no namespace")
	} else {
		namespace, err = namespaceObj.String()
	}
	klog.Infof("Retrieve config from %s in namespace %s", name, namespace)

	k8sClient := getClient()
	var configMap corev1.ConfigMap
	err = k8sClient.Get(context.Background(), client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, &configMap)
	if err != nil {
		return nil, err
	}
	kvConfig, ok := configMap.Data["input-properties"]
	if ok {
		inputProperties := map[string]any{}
		err := json.Unmarshal([]byte(kvConfig), &inputProperties)
		if err != nil {
			return nil, err
		}
		klog.Infof("Input Properties Parsed: %s", inputProperties)
		return inputProperties, nil
	}
	return configMap.Data, nil
}

func newCmd(_ cue.Value) (registry.Runner, error) {
	return &Cmd{}, nil
}

func Process(val cue.Value) (cue.Value, error) {
	config := val.LookupPath(value.FieldPath("config"))
	fields, _ := config.Fields()
	for {
		if !fields.Next() {
			break
		}
		configKey := fields.Label()
		configObj := fields.Value()

		klog.Infof("Processing Configuration for: %s", configKey)
		resp, _ := exec(configObj)
		val = val.FillPath(value.FieldPath("config", configKey, "output"), struct{}{})
		for k, v := range resp {
			klog.Infof("Adding %s with value %s", k, v)
			val = val.FillPath(value.FieldPath("config", configKey, "output", k), v)
		}
	}
	return val, nil
}

func exec(v cue.Value) (map[string]string, error) {
	config, _ := builtin.RunTaskByKey("config", cue.Value{}, &registry.Meta{Obj: v})
	configMap, ok := config.(map[string]string)
	if !ok {
		return nil, fmt.Errorf("failed to convert config to map[string]string")
	}
	return configMap, nil
}

func getClient() client.Client {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(homeDir, ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("Failed to load kubeconfig: %v", err)
	}

	k8sClient, err := client.New(config, client.Options{})
	return k8sClient
}
