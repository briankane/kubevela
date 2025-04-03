package configreader

import (
	"context"
	"github.com/oam-dev/kubevela/pkg/workflow/providers/types"
	types2 "k8s.io/apimachinery/pkg/types"
)

func ReadConfig(ctx context.Context, namespace string, name string) (map[string]interface{}, error) {
	configFactory := types.Params[types2.NamespacedName]{
		Params: types2.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}

	content, err := configFactory.ConfigFactory.ReadConfig(ctx, namespace, name)
	if err != nil {
		return make(map[string]interface{}), err
	}
	return content, nil
}
