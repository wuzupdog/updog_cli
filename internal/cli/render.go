package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type logEnvelope struct {
	Data []struct {
		LoggedAt string `json:"logged_at"`
		Level    string `json:"level"`
		Hostname string `json:"hostname"`
		Message  string `json:"message"`
	} `json:"data"`
	Meta responseMeta `json:"meta"`
}

type errorGroup struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	LastSeenAt      string `json:"last_seen_at"`
	OccurrenceCount int64  `json:"occurrence_count"`
	ErrorClass      string `json:"error_class"`
	ErrorMessage    string `json:"error_message"`
}

type errorEnvelope struct {
	Data []errorGroup `json:"data"`
	Meta responseMeta `json:"meta"`
}

type errorDetailEnvelope struct {
	Data struct {
		errorGroup
		Occurrences []struct {
			OccurredAt string `json:"occurred_at"`
			Hostname   string `json:"hostname"`
			Message    string `json:"message"`
		} `json:"occurrences"`
	} `json:"data"`
	Meta responseMeta `json:"meta"`
}

type responseMeta struct {
	Pagination struct {
		Total   int64 `json:"total"`
		Limit   int64 `json:"limit"`
		Offset  int64 `json:"offset"`
		HasMore bool  `json:"has_more"`
	} `json:"pagination"`
}

func renderAPIResponse(out io.Writer, kind string, body []byte, asJSON bool) error {
	if asJSON {
		_, err := fmt.Fprintln(out, string(compactJSON(body)))
		return err
	}

	switch kind {
	case "logs":
		return renderLogs(out, body)
	case "errors":
		return renderErrors(out, body)
	case "error":
		return renderErrorDetail(out, body)
	default:
		_, err := fmt.Fprintln(out, string(compactJSON(body)))
		return err
	}
}

func renderLogs(out io.Writer, body []byte) error {
	var envelope logEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode log response: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tLEVEL\tHOST\tMESSAGE")
	for _, row := range envelope.Data {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.LoggedAt, row.Level, row.Hostname, oneLine(row.Message, 100))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return renderCount(out, int64(len(envelope.Data)), envelope.Meta)
}

func renderErrors(out io.Writer, body []byte) error {
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode error response: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tLAST SEEN\tCOUNT\tCLASS\tMESSAGE")
	for _, row := range envelope.Data {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\n", row.ID, row.Status, row.LastSeenAt, row.OccurrenceCount, row.ErrorClass, oneLine(row.ErrorMessage, 80))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return renderCount(out, int64(len(envelope.Data)), envelope.Meta)
}

func renderErrorDetail(out io.Writer, body []byte) error {
	var envelope errorDetailEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode error detail response: %w", err)
	}
	fmt.Fprintf(out, "#%d %s: %s\n", envelope.Data.ID, envelope.Data.ErrorClass, envelope.Data.ErrorMessage)
	fmt.Fprintf(out, "Status: %s  Last seen: %s  Total occurrences: %d\n\n", envelope.Data.Status, envelope.Data.LastSeenAt, envelope.Data.OccurrenceCount)

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "OCCURRED AT\tHOST\tMESSAGE")
	for _, row := range envelope.Data.Occurrences {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", row.OccurredAt, row.Hostname, oneLine(row.Message, 100))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return renderCount(out, int64(len(envelope.Data.Occurrences)), envelope.Meta)
}

func renderCount(out io.Writer, shown int64, meta responseMeta) error {
	_, err := fmt.Fprintf(out, "\nShowing %d of %d", shown, meta.Pagination.Total)
	if meta.Pagination.HasMore {
		_, err = fmt.Fprintf(out, " (more available)")
	}
	if err == nil {
		_, err = fmt.Fprintln(out)
	}
	return err
}

func oneLine(value string, maximum int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum-1]) + "…"
}
