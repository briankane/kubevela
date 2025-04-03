package configreader

import (
	"context"
	"github.com/kubevela/pkg/util/singleton"
	"github.com/oam-dev/kubevela/pkg/config/common"
)

func ReadConfig(ctx context.Context, namespace string, name string) (map[string]interface{}, error) {
	content, err := common.ReadConfig(ctx, singleton.KubeClient.Get(), namespace, name)
	if err != nil {
		return make(map[string]interface{}), err
	}
	return content, nil
}
