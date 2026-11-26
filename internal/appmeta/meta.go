package appmeta

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

//go:embed identity.mk
var identitySource string

var (
	commandNamePattern = regexp.MustCompile("^[a-z][a-z0-9_]{0,62}$")
	displayNamePattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$")
	identity           = mustParseIdentity(identitySource)
)

// Identity is the command name and display name from an identity file.
type Identity struct {
	CommandName string
	RuntimeName string
}

// CommandName is the CLI name from APP_NAME.
func CommandName() string { return identity.CommandName }

// RuntimeName is the display name from APP_DISPLAY_NAME.
func RuntimeName() string { return identity.RuntimeName }

// ValidateCommandName checks the shared APP_NAME syntax.
func ValidateCommandName(name string) error {
	if !commandNamePattern.MatchString(name) {
		return fmt.Errorf("APP_NAME must match %s", commandNamePattern)
	}
	return nil
}

// ParseIdentity reads literal Make assignments and validates both identity fields.
func ParseIdentity(source string) (Identity, error) {
	var result Identity
	for line := range strings.SplitSeq(source, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasSuffix(line, "\\")) {
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			return Identity{}, errors.New("expected a literal KEY = value assignment or a full-line comment")
		}
		switch key {
		case "APP_NAME":
			if result.CommandName != "" {
				return Identity{}, errors.New("duplicate APP_NAME")
			}
			if err := ValidateCommandName(value); err != nil {
				return Identity{}, err
			}
			result.CommandName = value
		case "APP_DISPLAY_NAME":
			if result.RuntimeName != "" {
				return Identity{}, errors.New("duplicate APP_DISPLAY_NAME")
			}
			if !displayNamePattern.MatchString(value) || strings.HasSuffix(value, " ") {
				return Identity{}, fmt.Errorf("APP_DISPLAY_NAME must match %s with no trailing space", displayNamePattern)
			}
			result.RuntimeName = value
		default:
			return Identity{}, fmt.Errorf("unexpected identity key %q", key)
		}
	}
	if result.CommandName == "" || result.RuntimeName == "" {
		return Identity{}, errors.New("APP_NAME and APP_DISPLAY_NAME are both required")
	}
	return result, nil
}

func mustParseIdentity(source string) Identity {
	value, err := ParseIdentity(source)
	if err != nil {
		panic(fmt.Errorf("invalid embedded identity.mk: %w", err))
	}
	return value
}
