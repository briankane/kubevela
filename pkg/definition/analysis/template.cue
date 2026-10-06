// What a definition's template may hold, by type: the top-level fields the
// controller reads, and their shape. Any other top-level field is a helper and
// is left alone. parameter is checked apart: unifying it here would open the
// closed copy that catches a read of a parameter it does not declare.

#velaObject: {apiVersion?: string, kind?: string, ...}

#velaErrs: [...string]

#velaInherit: bool | {[string]: bool}

// #velaSchema describes what a definition provides. Any type may declare one;
// a source must.
#velaSchema: {...}

#velaTemplates: {
	component: {
		schema?: #velaSchema
		output?: #velaObject
		outputs?: [string]: #velaObject
		errs?:     #velaErrs
		"$super"?: {...}
		"$inherit"?: #velaInherit
		...
	}
	trait: {
		schema?: #velaSchema
		errs?:       #velaErrs
		processing?: {...}
		outputs?: [string]: #velaObject
		patch?: {...}
		patchOutputs?: [string]: {...}
		"$super"?: {...}
		"$inherit"?: #velaInherit
		...
	}
	policy: {
		schema?: #velaSchema
		output?: #velaObject
		outputs?: [string]: #velaObject
		errs?: #velaErrs
		...
	}
	// A policy with attributes: scope: "Application" transforms the Application:
	// its output may only say what to change.
	"application-policy": {
		schema?: #velaSchema
		config?: {enabled?: bool, ...}
		enabled?: bool
		output?: {
			components?: [...{...}]
			workflow?: {...}
			policies?: [...{...}]
			labels?: [string]:      string
			annotations?: [string]: string
			ctx?: {...}
		}
		...
	}
	"workflow-step": {
		schema?: #velaSchema
		...
	}
	source: {
		schema?: #velaSchema
		output?: {...}
		errs?: #velaErrs
		"$internal"?: {key?: string, keyInputs?: [...string], ...}
		storage?: {
			storageTTL?:     string
			onStaleFailure?: "use-stale" | "fail"
		}
		...
	}
	workload: {
		schema?: #velaSchema
		...
	}
}
