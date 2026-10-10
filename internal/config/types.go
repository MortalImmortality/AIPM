package config

type Snapshot map[string]any

type Status struct {
	Kind        string
	ActiveID    string
	Label       string
	Fingerprint string
	Warnings    []string
}

type Change struct {
	Bytes    []byte
	Snapshot Snapshot
	Fields   []string
	Warnings []string
}
