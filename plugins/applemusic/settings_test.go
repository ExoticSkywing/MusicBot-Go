package applemusic

import "testing"

func TestPreferAtmosDefinitionDefaultsOff(t *testing.T) {
	definition := PreferAtmosDefinition()

	if definition.Plugin != "applemusic" || definition.Key != PreferAtmosKey {
		t.Fatalf("setting identity = %s.%s", definition.Plugin, definition.Key)
	}
	if definition.DefaultUser != PreferAtmosOff || definition.DefaultGroup != PreferAtmosOff {
		t.Fatalf("defaults = user:%q group:%q, want both off", definition.DefaultUser, definition.DefaultGroup)
	}
	if !definition.Validate(PreferAtmosOff) || !definition.Validate(PreferAtmosOn) {
		t.Fatal("on/off setting values must be valid")
	}
	if definition.Validate("") || definition.Validate("true") {
		t.Fatal("empty and legacy boolean values must not be valid")
	}
	if definition.TitleKey == "" || definition.DescriptionKey == "" {
		t.Fatal("setting must provide localization keys")
	}
}

func TestContributionOnlyOffersPreferAtmosWhenAtmosIsUsable(t *testing.T) {
	withoutWrapper := contributionForClient(&Client{})
	if len(withoutWrapper.SettingDefinitions) != 0 {
		t.Fatalf("settings without wrapper = %+v, want none", withoutWrapper.SettingDefinitions)
	}

	withWrapper := contributionForClient(&Client{wrapperHost: " wrapper.test "})
	if len(withWrapper.SettingDefinitions) != 1 {
		t.Fatalf("settings with wrapper = %+v, want one", withWrapper.SettingDefinitions)
	}
	if got := withWrapper.SettingDefinitions[0]; got.Plugin != "applemusic" || got.Key != PreferAtmosKey {
		t.Fatalf("unexpected setting registered: %+v", got)
	}
}
