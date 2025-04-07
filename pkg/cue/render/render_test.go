package render

import (
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

	render, err := ComponentEngine.Render(ctx, dedent.Dedent(`
		config: {
			test: {
				name: "quadrant"
				namespace: "vela-system"
			}
		}

		test: {
			value: "hello-world"
		}

		parameter: {
			"param1": string | "default"
			"param2": string
		}

		output: {
			"apiVersion": "v1"
			"kind": "ConfigMap"
			"metadata": {
				"name": test.value
			}
			data: {
				value: config.test."quadrant-name"
			}
		}
	`), map[string]interface{}{
		"param1": "value",
		"param2": "another",
	})
	if err != nil {
		return
	}

	expected := strings.TrimSpace(dedent.Dedent(`
		// Context Definition
		context: [string]: _

		// Context Values
		context: {
			appAnnotations: null
			appLabels:      null
			appName:        "test-app"
			appRevision:    "test-app-v1"
			appRevisionNum: 1
			cluster:        ""
			clusterVersion: {
				gitVersion: ""
				major:      ""
				minor:      19
				platform:   ""
			}
			components:     null
			name:           "test"
			namespace:      "default"
			publishVersion: ""
			replicaKey:     ""
			revision:       ""
			workflowName:   ""
		}

		// Configuration Definition
		config: [string]: _

		// Configuration Values
		config: {
			test: {
				"quadrant-name": "dev"
			}
		}

		// Parameter Definition
		parameter: {
			param1: string | "default"
			param2: string
		}

		// Parameter Values
		parameter: {
			param1: "value"
			param2: "another"
		}

		// Fields
		test: {
			value: "hello-world"
		}

		
		// Output
		output: {
			apiVersion: "v1"
			kind:       "ConfigMap"
			metadata: {
				name: test.value
			}
			data: {
				value: config.test."quadrant-name"
			}
		}

		// Outputs (Ancillary)
		outputs: {}
	`))

	assert.Equal(t, expected, render.StrValue())

	compiled, _ := render.Compile(ctx)
	err = compiled.Validate()
	assert.NoError(t, err)
}

func TestComponentRenderer_NoParams(t *testing.T) {
	ctx := process.NewContext(process.ContextData{
		AppName:         "test-app",
		CompName:        "test",
		Namespace:       "default",
		AppRevisionName: "test-app-v1",
		ClusterVersion:  types.ClusterVersion{Minor: "19+"},
	})

	render, err := ComponentEngine.Render(ctx, strings.TrimSpace(dedent.Dedent(`
	context: {appName: "test-app"}
		config: {
			test: {
				name: "quadrant"
				namespace: "vela-system"
			}
		}
		parameter: {}
	`)), map[string]interface{}{})
	if err != nil {
		return
	}

	expected := strings.TrimSpace(dedent.Dedent(`
		// Context Definition
		context: [string]: _

		// Context Values
		context: {
			appAnnotations: null
			appLabels:      null
			appName:        "test-app"
			appRevision:    "test-app-v1"
			appRevisionNum: 1
			cluster:        ""
			clusterVersion: {
				gitVersion: ""
				major:      ""
				minor:      19
				platform:   ""
			}
			components:     null
			name:           "test"
			namespace:      "default"
			publishVersion: ""
			replicaKey:     ""
			revision:       ""
			workflowName:   ""
		}

		// Configuration Definition
		config: [string]: _

		// Configuration Values
		config: {
			test: {
				"quadrant-name": "dev"
			}
		}

		// Parameter Definition
		parameter: {}

		// Parameter Values
		parameter: {}

		// Fields


		// Output
		output: {}

		// Outputs (Ancillary)
		outputs: {}
	`))

	assert.Equal(t, expected, render.StrValue())
}

func TestComponentRenderer_BlankParams(t *testing.T) {
	ctx := process.NewContext(process.ContextData{
		AppName:         "test-app",
		CompName:        "test",
		Namespace:       "default",
		AppRevisionName: "test-app-v1",
		ClusterVersion:  types.ClusterVersion{Minor: "19+"},
	})

	render, err := ComponentEngine.Render(ctx, strings.TrimSpace(dedent.Dedent(`
		context: {appName: "test-app"}
		config: {
			test: {
				name: "quadrant"
				namespace: "vela-system"
			}
		}
	`)), map[string]interface{}{})
	if err != nil {
		return
	}

	expected := strings.TrimSpace(dedent.Dedent(`
		// Context Definition
		context: [string]: _

		// Context Values
		context: {
			appAnnotations: null
			appLabels:      null
			appName:        "test-app"
			appRevision:    "test-app-v1"
			appRevisionNum: 1
			cluster:        ""
			clusterVersion: {
				gitVersion: ""
				major:      ""
				minor:      19
				platform:   ""
			}
			components:     null
			name:           "test"
			namespace:      "default"
			publishVersion: ""
			replicaKey:     ""
			revision:       ""
			workflowName:   ""
		}

		// Configuration Definition
		config: [string]: _

		// Configuration Values
		config: {
			test: {
				"quadrant-name": "dev"
			}
		}

		// Parameter Definition
		parameter: {}

		// Parameter Values
		parameter: {}

		// Fields


		// Output
		output: {}

		// Outputs (Ancillary)
		outputs: {}
	`))

	assert.Equal(t, expected, render.StrValue())
}
