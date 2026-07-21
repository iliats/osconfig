package scenario

import (
	"reflect"
	"testing"
)

func TestParseCategories(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string
		wantErr bool
	}{
		{name: "all by default", want: []string{"compatibility", "functional", "install-upgrade"}},
		{name: "selected", value: "functional, compatibility", want: []string{"compatibility", "functional"}},
		{name: "reject unknown", value: "weekly", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCategories(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseCategories() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && !reflect.DeepEqual(Names(got), tc.want) {
				t.Fatalf("ParseCategories() = %v, want %v", Names(got), tc.want)
			}
		})
	}
}
