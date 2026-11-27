package target

import (
	"bufio"
	"bytes"
	"strings"
)

// samplesOf returns the label sets of every sample of one metric in a
// Prometheus text exposition. Only labels are needed: vLLM's info metrics
// carry their data in labels and always have the value 1.
func samplesOf(exposition []byte, metricName string) []map[string]string {
	var samples []map[string]string
	scanner := bufio.NewScanner(bytes.NewReader(exposition))
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, metricName+"{") {
			continue
		}
		if labels, ok := parseLabels(line[len(metricName)+1:]); ok {
			samples = append(samples, labels)
		}
	}
	return samples
}

// parseLabels reads `name="value",...}` up to the closing brace, with the
// exposition format's \\, \" and \n escapes.
func parseLabels(text string) (map[string]string, bool) {
	labels := map[string]string{}
	for {
		text = strings.TrimLeft(text, ", ")
		if strings.HasPrefix(text, "}") {
			return labels, true
		}
		name, rest, found := strings.Cut(text, `="`)
		if !found {
			return nil, false
		}
		var value strings.Builder
		index := 0
		for ; index < len(rest) && rest[index] != '"'; index++ {
			if rest[index] != '\\' || index+1 == len(rest) {
				value.WriteByte(rest[index])
				continue
			}
			index++
			switch rest[index] {
			case 'n':
				value.WriteByte('\n')
			default:
				value.WriteByte(rest[index])
			}
		}
		if index == len(rest) {
			return nil, false
		}
		labels[strings.TrimSpace(name)] = value.String()
		text = rest[index+1:]
	}
}
