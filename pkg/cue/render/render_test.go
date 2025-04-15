package render

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"

	"github.com/lithammer/dedent"
	"github.com/stretchr/testify/assert"

	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/cue/process"
)

func TestComponentRenderer(t *testing.T) {
	ctx := process.NewContext(process.ContextData{
		AppName:         "test-app",
		CompName:        "test",
		Namespace:       "default",
		AppRevisionName: "test-app-v1",
		ClusterVersion:  types.ClusterVersion{Minor: "19+"},
	})

	_, err := ComponentRenderer(ctx).Render(dedent.Dedent(`
		import (
			"guidewire/data"
		)

		$config: {
			test: {
				name: "quadrant"
				namespace: "vela-system"
			}
		}

		$data: {
			"external-data": {
				provider: "cuex-package-guidewire-data"
				function: "getQuadrant"
				params: {
					quadrant: $config.test.output."quadrant-name"
				}
			}
		}

		simple: "simple string"

		test: {
			value: "hello-world"
		} @test(hello)

		another: {
			value: "another-\(test.value)"
		}

		parameter: {
			"param1": string | context.name
			"param2": string
		}

		output: {
			"apiVersion": "v1"
			"kind": "ConfigMap"
			"metadata": {
				"name": test.value
			}
			data: {
				value: $config.test.output."quadrant-name"
			}
		}
	`), map[string]interface{}{
		"param1": "value",
		"param2": "another",
	})
	require.NoError(t, err)

	//expected := strings.TrimSpace(dedent.Dedent(`
	//	// Context Definition
	//	context: [string]: _
	//
	//	// Context Values
	//	context: {
	//		appAnnotations: null
	//		appLabels:      null
	//		appName:        "test-app"
	//		appRevision:    "test-app-v1"
	//		appRevisionNum: 1
	//		cluster:        ""
	//		clusterVersion: {
	//			gitVersion: ""
	//			major:      ""
	//			minor:      19
	//			platform:   ""
	//		}
	//		components:     null
	//		name:           "test"
	//		namespace:      "default"
	//		publishVersion: ""
	//		replicaKey:     ""
	//		revision:       ""
	//		workflowName:   ""
	//	}
	//
	//	// Configuration Definition
	//	$config: [string]: _
	//
	//	// Configuration Values
	//	$config: {
	//		test: {
	//			"quadrant-name": "dev"
	//		}
	//	}
	//
	//	// Data Definition
	//	$data: [string]: _
	//
	//	// Data Values
	//	$data: {}
	//
	//	// Parameter Definition
	//	parameter: {
	//		param1: string | "default"
	//		param2: string
	//	}
	//
	//	// Parameter Values
	//	parameter: {
	//		param1: "value"
	//		param2: "another"
	//	}
	//
	//	// Fields
	//	test: {
	//		value: "hello-world"
	//	}
	//
	//	another: {
	//		value: "another-hello-world"
	//	}
	//
	//	// Output
	//	output: {
	//		apiVersion: "v1"
	//		kind:       "ConfigMap"
	//		metadata: {
	//			name: test.value
	//		}
	//		data: {
	//			value: $config.test."quadrant-name"
	//		}
	//	}
	//
	//	// Outputs
	//	outputs: {}
	//`))

	//assert.Equal(t, expected, render.StrValue())

	//compiled, _ := render.Compile(ctx)
	//err = compiled.Validate()
	//assert.NoError(t, err)
}

func TestComponentRenderer_NoParams(t *testing.T) {
	ctx := process.NewContext(process.ContextData{
		AppName:         "test-app",
		CompName:        "test",
		Namespace:       "default",
		AppRevisionName: "test-app-v1",
		ClusterVersion:  types.ClusterVersion{Minor: "19+"},
	})

	_, err := ComponentRenderer(ctx).Render(strings.TrimSpace(dedent.Dedent(`
	context: {appName: "test-app"}
		$config: {
			test: {
				name: "quadrant"
				namespace: "vela-system"
			}
		}
		parameter: {}
	`)), map[string]interface{}{})
	assert.NoError(t, err)

	//expected := strings.TrimSpace(dedent.Dedent(`
	//	// Context Definition
	//	context: [string]: _
	//
	//	// Context Values
	//	context: {
	//		appAnnotations: null
	//		appLabels:      null
	//		appName:        "test-app"
	//		appRevision:    "test-app-v1"
	//		appRevisionNum: 1
	//		cluster:        ""
	//		clusterVersion: {
	//			gitVersion: ""
	//			major:      ""
	//			minor:      19
	//			platform:   ""
	//		}
	//		components:     null
	//		name:           "test"
	//		namespace:      "default"
	//		publishVersion: ""
	//		replicaKey:     ""
	//		revision:       ""
	//		workflowName:   ""
	//	}
	//
	//	// Configuration Definition
	//	$config: [string]: _
	//
	//	// Configuration Values
	//	$config: {
	//		test: {
	//			"quadrant-name": "dev"
	//		}
	//	}
	//
	//	// Data Definition
	//	$data: [string]: _
	//
	//	// Data Values
	//	$data: {}
	//
	//	// Parameter Definition
	//	parameter: {}
	//
	//	// Parameter Values
	//	parameter: {}
	//
	//	// Output
	//	output: {}
	//
	//	// Outputs
	//	outputs: {}
	//`))

	//assert.Equal(t, expected, render.StrValue())
}

func TestComponentRenderer_BlankParams(t *testing.T) {
	ctx := process.NewContext(process.ContextData{
		AppName:         "test-app",
		CompName:        "test",
		Namespace:       "default",
		AppRevisionName: "test-app-v1",
		ClusterVersion:  types.ClusterVersion{Minor: "19+"},
	})

	_, err := ComponentRenderer(ctx).Render(strings.TrimSpace(dedent.Dedent(`
		context: {appName: "test-app"}
		$config: {
			test: {
				name: "quadrant"
				namespace: "vela-system"
			}
		}
	`)), map[string]interface{}{})
	assert.NoError(t, err)

	//expected := strings.TrimSpace(dedent.Dedent(`
	//	// Context Definition
	//	context: [string]: _
	//
	//	// Context Values
	//	context: {
	//		appAnnotations: null
	//		appLabels:      null
	//		appName:        "test-app"
	//		appRevision:    "test-app-v1"
	//		appRevisionNum: 1
	//		cluster:        ""
	//		clusterVersion: {
	//			gitVersion: ""
	//			major:      ""
	//			minor:      19
	//			platform:   ""
	//		}
	//		components:     null
	//		name:           "test"
	//		namespace:      "default"
	//		publishVersion: ""
	//		replicaKey:     ""
	//		revision:       ""
	//		workflowName:   ""
	//	}
	//
	//	// Configuration Definition
	//	$config: [string]: _
	//
	//	// Configuration Values
	//	$config: {
	//		test: {
	//			"quadrant-name": "dev"
	//		}
	//	}
	//
	//	// Data Definition
	//	$data: [string]: _
	//
	//	// Data Values
	//	$data: {}
	//
	//	// Parameter Definition
	//	parameter: {}
	//
	//	// Parameter Values
	//	parameter: {}
	//
	//	// Output
	//	output: {}
	//
	//	// Outputs
	//	outputs: {}
	//`))

	//assert.Equal(t, expected, render.StrValue())
}
