/*
 * Tsuru
 *
 * Open source, extensible and Docker-based Platform as a Service (PaaS)
 *
 * API version: 1.6
 * Generated by: Swagger Codegen (https://github.com/swagger-api/swagger-codegen.git)
 */

package tsuru

// Volume
type Volume struct {

	// Volume name.
	Name string `json:"name,omitempty"`

	// Volume pool.
	Pool string `json:"pool,omitempty"`

	// Team that owns the volume.
	TeamOwner string `json:"teamOwner,omitempty"`

	// Volume status.
	Status string `json:"status,omitempty"`

	// Volume plan.
	Plan *VolumePlan `json:"plan,omitempty"`

	// Volume binds.
	Binds []VolumeBind `json:"binds,omitempty"`

	// Custom volume options.
	Opts map[string]string `json:"opts,omitempty"`
}
