package addon

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"
	"github.com/kubevela/pkg/util/singleton"
	providertypes "github.com/kubevela/workflow/pkg/providers/types"
	common2 "github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/addon"
	addonutil "github.com/oam-dev/kubevela/pkg/utils/addon"
	"github.com/oam-dev/kubevela/pkg/utils/apply"
	types2 "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"time"
)

type Params struct {
	Name           string                 `json:"name"`
	Version        string                 `json:"version"`
	OverrideDefs   bool                   `json:"overrideDefs,omitempty"`
	SkipValidation bool                   `json:"skipValidation,omitempty"`
	Args           map[string]interface{} `json:"args,omitempty"`
}

type Returns struct {
	Installed bool   `json:"installed"`
	AppName   string `json:"appName,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type EnableParams = providertypes.Params[Params]
type EnableReturns = providertypes.Returns[Returns]

// EnableAddon enables an addon with the given parameters.
func EnableAddon(ctx context.Context, params *EnableParams) (*providertypes.Returns[Returns], error) {
	k8s := singleton.KubeClient.Get()
	cfg := singleton.KubeConfig.Get()
	dc, _ := discovery.NewDiscoveryClientForConfig(singleton.KubeConfig.Get())
	applicator := apply.NewAPIApplicator(k8s)

	var err error
	registryDS := addon.NewRegistryDataStore(k8s)
	registries, err := registryDS.ListRegistries(ctx)
	if err != nil {
		return &EnableReturns{Returns: Returns{Installed: false}}, err
	}
	registryName, addonName, err := splitSpecifyRegistry(params.Params.Name)
	if err != nil {
		return &EnableReturns{Returns: Returns{Installed: false}}, err
	}
	if len(registryName) != 0 {
		foundRegistry := false
		for _, registry := range registries {
			if registry.Name == registryName {
				foundRegistry = true
			}
		}
		if !foundRegistry {
			return &EnableReturns{Returns: Returns{Installed: false}}, fmt.Errorf("specified registry %s not exist", registryName)
		}
	}
	for i, registry := range registries {
		opts := addonOptions(params.Params)
		if len(registryName) != 0 && registryName != registry.Name {
			continue
		}
		_, err = addon.EnableAddon(ctx, addonName, params.Params.Version, k8s, dc, applicator, cfg, registry, params.Params.Args, nil, addon.FilterDependencyRegistries(i, registries), opts...)
		if errors.Is(err, addon.ErrNotExist) || errors.Is(err, addon.ErrFetch) {
			continue
		}
		if err != nil {
			return &EnableReturns{Returns: Returns{Installed: false}}, err
		}
		if ok := waitApplicationRunning(k8s, addonName); ok {
			return &EnableReturns{Returns: Returns{
				Installed: true,
				AppName:   addonutil.Addon2AppName(addonName),
				Namespace: types.DefaultKubeVelaNS,
			}}, nil
		} else {
			return &EnableReturns{Returns: Returns{Installed: false}}, fmt.Errorf("addon: %s failed to enable, please check the application status", addonName)
		}
	}
	return &EnableReturns{Returns: Returns{Installed: false}}, fmt.Errorf("addon %s not found in any registries, please check the addon name or registry name", params.Params.Name)
}

func splitSpecifyRegistry(name string) (string, string, error) {
	res := strings.Split(name, "/")
	switch len(res) {
	case 2:
		return res[0], res[1], nil
	case 1:
		return "", res[0], nil
	default:
		return "", "", fmt.Errorf("invalid addon name, you should specify name only  <addonName>  or with registry as prefix <registryName>/<addonName>")
	}
}

func addonOptions(params Params) []addon.InstallOption {
	var opts []addon.InstallOption
	opts = append(opts, addon.SkipValidateVersion)
	opts = append(opts, addon.OverrideDefinitions)

	if params.SkipValidation {
		opts = append(opts, addon.SkipValidateVersion)
	}
	if params.OverrideDefs {
		opts = append(opts, addon.OverrideDefinitions)
	}
	return opts
}

func waitApplicationRunning(k8sClient client.Client, addonName string) bool {
	trackInterval := 5 * time.Second
	timeout := 600 * time.Second
	start := time.Now()
	ctx := context.Background()
	var app v1beta1.Application

	for {
		err := k8sClient.Get(ctx, types2.NamespacedName{Name: addonutil.Addon2AppName(addonName), Namespace: types.DefaultKubeVelaNS}, &app)
		if err != nil {
			return false
		}

		switch app.Status.Phase {
		case common2.ApplicationRunning:
			return true
		case common2.ApplicationWorkflowSuspending:
			fmt.Printf("Enabling suspend, please run \"vela workflow resume %s -n vela-system\" to continue", addonutil.Addon2AppName(addonName))
			return true
		case common2.ApplicationWorkflowTerminated, common2.ApplicationWorkflowFailed:
			return false
		default:
		}

		timeConsumed := int(time.Since(start).Seconds())
		if timeConsumed > int(timeout.Seconds()) {
			return false
		}
		time.Sleep(trackInterval)
	}
}

//go:embed addon.cue
var template string

// GetTemplate returns the template
func GetTemplate() string {
	return template
}

// GetProviders returns the provider
func GetProviders() map[string]cuexruntime.ProviderFn {
	return map[string]cuexruntime.ProviderFn{
		"enable": providertypes.GenericProviderFn[Params, providertypes.Returns[Returns]](EnableAddon),
	}
}
