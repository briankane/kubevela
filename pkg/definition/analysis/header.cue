// The definition header, as Definition.FromCUE reads it: one top-level field
// named after the definition. #attributes, generated from the Definition CRDs,
// gives what attributes may hold for each type.
#header: {
	type!:        "component" | "trait" | "policy" | "workflow-step" | "source" | "workload"
	alias?:       string
	description?: string
	annotations?: [string]: string
	labels?: [string]:      string
	// extends and abstract are spec fields that may also be written here.
	extends?:    string
	abstract?:   bool
	attributes?: _
}
