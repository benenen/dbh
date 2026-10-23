package redis

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// splitArgs follows redis-cli quoting: double quotes accept \n, \r, \t, \b,
// \a and \xHH escapes, single quotes accept only \'.
func splitArgs(line string) ([]string, error) {
	var args []string
	for i := 0; ; {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i == len(line) {
			return args, nil
		}
		var arg strings.Builder
		quote := byte(0)
		for done := false; !done; {
			if i == len(line) {
				if quote != 0 {
					return nil, fmt.Errorf("unbalanced quotes in Redis command")
				}
				break
			}
			c := line[i]
			switch {
			case quote == '"' && c == '\\' && i+3 < len(line) && line[i+1] == 'x' && isHex(line[i+2]) && isHex(line[i+3]):
				b, _ := strconv.ParseUint(line[i+2:i+4], 16, 8)
				arg.WriteByte(byte(b))
				i += 3
			case quote == '"' && c == '\\' && i+1 < len(line):
				i++
				if j := strings.IndexByte("nrtba", line[i]); j >= 0 {
					arg.WriteByte("\n\r\t\b\a"[j])
				} else {
					arg.WriteByte(line[i])
				}
			case quote == '\'' && c == '\\' && i+1 < len(line) && line[i+1] == '\'':
				i++
				arg.WriteByte('\'')
			case quote != 0 && c == quote:
				// A closing quote must end the argument.
				if i+1 < len(line) && !isSpace(line[i+1]) {
					return nil, fmt.Errorf("closing quote must be followed by a space in Redis command")
				}
				done = true
			case quote != 0:
				arg.WriteByte(c)
			case isSpace(c):
				done = true
			case (c == '"' || c == '\'') && arg.Len() == 0:
				quote = c
			default:
				arg.WriteByte(c)
			}
			i++
		}
		args = append(args, arg.String())
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func jsonText(value any) string {
	text, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(text)
}
