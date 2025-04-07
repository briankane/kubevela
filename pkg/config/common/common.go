package common

import (
	"context"
	"encoding/json"
	"errors"
	v1 "k8s.io/api/core/v1"
	pkgtypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/types"
)

func ReadConfig(ctx context.Context, client client.Client, namespace string, name string) (map[string]interface{}, error) {
	var secret v1.Secret
	if err := client.Get(ctx, pkgtypes.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, err
	}
	if secret.Annotations[types.AnnotationConfigSensitive] == "true" {
		return nil, ErrSensitiveConfig
	}
	properties := secret.Data[SaveInputPropertiesKey]
	var input = map[string]interface{}{}
	if err := json.Unmarshal(properties, &input); err != nil {
		return nil, err
	}
	return input, nil
}

// ErrSensitiveConfig means this config can not be read directly.
var ErrSensitiveConfig = errors.New("the config is sensitive")

// SaveInputPropertiesKey define the key name for saving the input properties in the secret.
const SaveInputPropertiesKey = "input-properties"

// TemplateConfigMapNamePrefix the prefix of the configmap name.
const TemplateConfigMapNamePrefix = "config-template-"
