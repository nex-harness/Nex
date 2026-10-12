package protocol

// assemblyCase is filled in by the assembler tests (wire_assemble_test.go).
type assemblyCase struct {
	Name    string             `json:"name"`
	Options map[string]float64 `json:"options"`
	Times   []float64          `json:"times"`
	Wires   []string           `json:"wires"`
	Events  [][]struct {
		Kind         string `json:"kind"`
		DeliveryKind string `json:"deliveryKind"`
		FrameJSON    string `json:"frameJson"`
		Reason       string `json:"reason"`
		Ordinal      int64  `json:"ordinal"`
		ID           string `json:"id"`
	} `json:"events"`
	Stats struct {
		Assemblies         int `json:"assemblies"`
		StagedDecodedBytes int `json:"stagedDecodedBytes"`
	} `json:"stats"`
}
