package providermodelcatalog

type Catalog struct{}

type Model struct {
	ID            string
	Name          string
	Description   string
	ContextWindow int
	ToolCall      bool
	Reasoning     bool
	InputCost     float64
	OutputCost    float64
	Tags          []string
}

func New() *Catalog {
	return &Catalog{}
}

func Models(provider interface{}) []Model {
	return nil
}
