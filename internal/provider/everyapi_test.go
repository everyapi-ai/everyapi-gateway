package provider

import "testing"

func TestEveryAPIPreset(t *testing.T) {
	p, err := FromPreset("everyapi")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://api.everyapi.ai/v1" || p.Responses != p.Chat || p.Anthropic != "https://api.everyapi.ai" {
		t.Fatal("EveryAPI preset does not preserve its native protocol endpoints")
	}
	pr := Preset("everyapi")
	if pr.Kind != KindRelay || len(pr.Regions) != 2 || len(pr.Models) != 0 || pr.Catalog != "" {
		t.Fatal("EveryAPI must discover its own models and offer both regions")
	}
}
