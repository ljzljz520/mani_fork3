package dao

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEnv_ParseNodeEnv(t *testing.T) {
	tests := []struct {
		name     string
		input    yaml.Node
		expected []EnvVar
	}{
		{
			name: "basic env variables keep raw expressions and origin",
			input: yaml.Node{
				Content: []*yaml.Node{
					{Value: "KEY1"},
					{Value: "value1", Line: 2},
					{Value: "KEY2"},
					{Value: "$(echo 123)", Line: 3},
				},
			},
			expected: []EnvVar{
				{Name: "KEY1", Value: "value1", Context: "mani.yaml", Line: 2},
				{Name: "KEY2", Value: "$(echo 123)", Context: "mani.yaml", Line: 3},
			},
		},
		{
			name: "empty env",
			input: yaml.Node{
				Content: []*yaml.Node{},
			},
			expected: nil,
		},
		{
			name: "env with special characters",
			input: yaml.Node{
				Content: []*yaml.Node{
					{Value: "PATH"},
					{Value: "/usr/bin:/bin", Line: 10},
					{Value: "URL"},
					{Value: "http://example.com", Line: 11},
				},
			},
			expected: []EnvVar{
				{Name: "PATH", Value: "/usr/bin:/bin", Context: "mani.yaml", Line: 10},
				{Name: "URL", Value: "http://example.com", Context: "mani.yaml", Line: 11},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseNodeEnv(tt.input, "mani.yaml")
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEnv_MergeEnvs(t *testing.T) {
	tests := []struct {
		name     string
		inputs   [][]EnvVar
		expected []EnvVar
	}{
		{
			name: "basic merge",
			inputs: [][]EnvVar{
				{{Name: "KEY1", Value: "value1"}, {Name: "KEY2", Value: "value2"}},
				{{Name: "KEY3", Value: "value3"}},
			},
			expected: []EnvVar{
				{Name: "KEY1", Value: "value1"},
				{Name: "KEY2", Value: "value2"},
				{Name: "KEY3", Value: "value3"},
			},
		},
		{
			name: "override priority keeps raw expression of the first group",
			inputs: [][]EnvVar{
				{{Name: "KEY1", Value: "$(echo override)", Context: "task.yaml", Line: 4}},
				{{Name: "KEY1", Value: "original"}, {Name: "KEY2", Value: "value2"}},
			},
			expected: []EnvVar{
				{Name: "KEY1", Value: "$(echo override)", Context: "task.yaml", Line: 4},
				{Name: "KEY2", Value: "value2"},
			},
		},
		{
			name: "empty inputs",
			inputs: [][]EnvVar{
				{},
				{},
			},
			expected: nil,
		},
		{
			name: "trailing newlines are trimmed",
			inputs: [][]EnvVar{
				{{Name: "KEY1", Value: "value1\n"}, {Name: "KEY2", Value: "value2\n"}},
				{{Name: "KEY3", Value: "value3\n"}},
			},
			expected: []EnvVar{
				{Name: "KEY1", Value: "value1"},
				{Name: "KEY2", Value: "value2"},
				{Name: "KEY3", Value: "value3"},
			},
		},
		{
			name: "complex values",
			inputs: [][]EnvVar{
				{{Name: "PATH", Value: "/usr/bin:/bin"}, {Name: "URL", Value: "http://example.com"}},
				{{Name: "DEBUG", Value: "true"}, {Name: "PATH", Value: "/custom/path"}},
			},
			expected: []EnvVar{
				{Name: "PATH", Value: "/usr/bin:/bin"},
				{Name: "URL", Value: "http://example.com"},
				{Name: "DEBUG", Value: "true"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeEnvs(tt.inputs...)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEnvVar_DynamicCommand(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		cmd     string
		dynamic bool
	}{
		{name: "plain literal", value: "bar", dynamic: false},
		{name: "substitution", value: "$(date)", cmd: "date", dynamic: true},
		{name: "substitution with spaces", value: "$(echo 123) ", cmd: "", dynamic: false},
		{name: "only prefix", value: "$(date", dynamic: false},
		{name: "only suffix", value: "date)", dynamic: false},
		{name: "embedded substitution stays literal", value: "x$(date)y", dynamic: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := EnvVar{Value: tt.value}
			cmd, dynamic := e.DynamicCommand()
			if dynamic != tt.dynamic {
				t.Errorf("expected dynamic=%v, got %v", tt.dynamic, dynamic)
			}
			if dynamic && cmd != tt.cmd {
				t.Errorf("expected cmd=%q, got %q", tt.cmd, cmd)
			}
		})
	}
}
