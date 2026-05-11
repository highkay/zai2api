package internal

import "testing"

func TestIsValidModelOnlyKeepsGLM5Families(t *testing.T) {
	validModels := []string{
		"GLM-5",
		"glm-5",
		"GLM-5-Turbo",
		"GLM-5v-Turbo",
		"GLM-5.1",
		"GLM-5.1-thinking",
		"GLM-5-search",
	}
	for _, model := range validModels {
		if !IsValidModel(model) {
			t.Fatalf("expected model %s to stay valid", model)
		}
	}

	invalidModels := []string{
		"GLM-4.6",
		"GLM-4.7",
		"GLM-4.6-V",
		"glm-4.6v",
		"glm-4.7",
	}
	for _, model := range invalidModels {
		if IsValidModel(model) {
			t.Fatalf("expected model %s to be removed", model)
		}
	}
}

func TestUpdateDynamicMappingsOnlyAddsSupportedGLM5Families(t *testing.T) {
	mappingsLock.Lock()
	oldMappings := modelMappings
	modelMappings = make(map[string]ModelMapping)
	mappingsLock.Unlock()
	t.Cleanup(func() {
		mappingsLock.Lock()
		modelMappings = oldMappings
		mappingsLock.Unlock()
	})

	initBuiltinMappings()
	updateDynamicMappings([]ZAIModel{
		{ID: "GLM-5.1-Air", Name: "GLM-5.1-Air"},
		{ID: "glm-4.7", Name: "GLM-4.7"},
	})

	if GetUpstreamConfig("GLM-5.1-Air") == nil {
		t.Fatalf("expected dynamic GLM-5 family model to be registered")
	}
	if GetUpstreamConfig("glm-4.7") != nil {
		t.Fatalf("expected GLM-4 family model to be filtered out")
	}
}
