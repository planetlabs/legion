//go:generate go run k8s.io/code-generator/cmd/deepcopy-gen --input-dirs code.earth.planet.com/product/legion/internal/kubernetes --bounding-dirs code.earth.planet.com/product/legion/internal/kubernetes -h ../../HEADER -O zz_generated.deepcopy

package kubernetes
