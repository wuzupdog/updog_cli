package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"
)

type hostEnvelope struct {
	Data []hostSnapshot `json:"data"`
	Meta struct {
		Total int64 `json:"total"`
	} `json:"meta"`
}

type hostSnapshot struct {
	Hostname         string       `json:"hostname"`
	Service          string       `json:"service"`
	Environment      string       `json:"environment"`
	AgentVersion     string       `json:"agent_version"`
	LastSeenAt       string       `json:"last_seen_at"`
	UptimeSeconds    *float64     `json:"uptime_seconds"`
	LogicalCPUCount  *float64     `json:"logical_cpu_count"`
	CPUPercent       *float64     `json:"cpu_percent"`
	CPUIOWaitPercent *float64     `json:"cpu_iowait_percent"`
	DiskIOPercent    *float64     `json:"disk_io_percent"`
	Load             hostLoad     `json:"load"`
	Memory           hostCapacity `json:"memory"`
	Swap             hostCapacity `json:"swap"`
	FileDescriptors  struct {
		Used *float64 `json:"used"`
		Max  *float64 `json:"max"`
	} `json:"file_descriptors"`
	Network struct {
		ReceiveBytesPerSecond  *float64 `json:"receive_bytes_per_second"`
		TransmitBytesPerSecond *float64 `json:"transmit_bytes_per_second"`
	} `json:"network"`
	Filesystems []hostFilesystem `json:"filesystems"`
	Disks       []hostDisk       `json:"disks"`
	Interfaces  []hostInterface  `json:"interfaces"`
	Processes   []hostProcess    `json:"processes"`
}

type hostLoad struct {
	One     *float64 `json:"one"`
	Five    *float64 `json:"five"`
	Fifteen *float64 `json:"fifteen"`
}

type hostCapacity struct {
	Total       *float64 `json:"total"`
	Used        *float64 `json:"used"`
	Available   *float64 `json:"available"`
	Utilization *float64 `json:"utilization"`
}

type hostFilesystem struct {
	Mountpoint     string   `json:"mountpoint"`
	Device         string   `json:"device"`
	FilesystemType string   `json:"filesystem_type"`
	Total          *float64 `json:"total"`
	Used           *float64 `json:"used"`
	Available      *float64 `json:"available"`
	Utilization    *float64 `json:"utilization"`
}

type hostDisk struct {
	Device              string   `json:"device"`
	ReadBytesPerSecond  *float64 `json:"read_bytes_per_second"`
	WriteBytesPerSecond *float64 `json:"write_bytes_per_second"`
	IOUtilization       *float64 `json:"io_utilization"`
}

type hostInterface struct {
	Interface                string   `json:"interface"`
	ReceiveBytesPerSecond    *float64 `json:"receive_bytes_per_second"`
	TransmitBytesPerSecond   *float64 `json:"transmit_bytes_per_second"`
	ReceiveUtilization       *float64 `json:"receive_utilization"`
	TransmitUtilization      *float64 `json:"transmit_utilization"`
	ReceiveErrorsPerSecond   *float64 `json:"receive_errors_per_second"`
	TransmitErrorsPerSecond  *float64 `json:"transmit_errors_per_second"`
	ReceiveDroppedPerSecond  *float64 `json:"receive_dropped_per_second"`
	TransmitDroppedPerSecond *float64 `json:"transmit_dropped_per_second"`
}

type hostProcess struct {
	Process             string   `json:"process"`
	PID                 string   `json:"pid"`
	CPUPercent          *float64 `json:"cpu_percent"`
	MemoryRSS           *float64 `json:"memory_rss"`
	OpenFileDescriptors *float64 `json:"open_file_descriptors"`
}

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
	if kind == "host" {
		if err := validateHostResponse(body); err != nil {
			return err
		}
	}
	if asJSON {
		_, err := fmt.Fprintln(out, string(compactJSON(body)))
		return err
	}

	switch kind {
	case "hosts":
		return renderHosts(out, body)
	case "host":
		return renderHost(out, body)
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

func validateHostResponse(body []byte) error {
	envelope, err := decodeHosts(body)
	if err != nil {
		return err
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("host not found")
	}
	return nil
}

func renderHosts(out io.Writer, body []byte) error {
	envelope, err := decodeHosts(body)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSERVICE\tENVIRONMENT\tLAST SEEN\tCPU\tMEMORY\tLOAD 1/5/15\tAGENT")
	for _, host := range envelope.Data {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			host.Hostname, displayText(host.Service), displayText(host.Environment), host.LastSeenAt,
			formatPercent(host.CPUPercent), formatCapacitySummary(host.Memory), formatLoad(host.Load), displayText(host.AgentVersion))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "\nShowing %d of %d hosts\n", len(envelope.Data), envelope.Meta.Total)
	return err
}

func renderHost(out io.Writer, body []byte) error {
	envelope, err := decodeHosts(body)
	if err != nil {
		return err
	}
	host := envelope.Data[0]
	renderHostOverview(out, host)
	if err := renderFilesystems(out, host.Filesystems); err != nil {
		return err
	}
	if err := renderDisks(out, host.Disks); err != nil {
		return err
	}
	if err := renderInterfaces(out, host.Interfaces); err != nil {
		return err
	}
	return renderProcesses(out, host.Processes)
}

func decodeHosts(body []byte) (hostEnvelope, error) {
	var envelope hostEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return envelope, fmt.Errorf("decode host response: %w", err)
	}
	return envelope, nil
}

func renderHostOverview(out io.Writer, host hostSnapshot) {
	fmt.Fprintf(out, "%s\n", host.Hostname)
	fmt.Fprintf(out, "Service: %s  Environment: %s  Agent: %s\n", displayText(host.Service), displayText(host.Environment), displayText(host.AgentVersion))
	fmt.Fprintf(out, "Last seen: %s  Uptime: %s  Logical CPUs: %s\n", displayText(host.LastSeenAt), formatDuration(host.UptimeSeconds), formatNumber(host.LogicalCPUCount))
	fmt.Fprintf(out, "CPU: %s  I/O wait: %s  Load: %s\n", formatPercent(host.CPUPercent), formatPercent(host.CPUIOWaitPercent), formatLoad(host.Load))
	fmt.Fprintf(out, "Memory: %s  Swap: %s\n", formatCapacitySummary(host.Memory), formatCapacitySummary(host.Swap))
	fmt.Fprintf(out, "File descriptors: %s / %s  Disk I/O: %s\n", formatNumber(host.FileDescriptors.Used), formatNumber(host.FileDescriptors.Max), formatPercent(host.DiskIOPercent))
	fmt.Fprintf(out, "Network: %s receive / %s transmit\n", formatRate(host.Network.ReceiveBytesPerSecond), formatRate(host.Network.TransmitBytesPerSecond))
}

func renderFilesystems(out io.Writer, rows []hostFilesystem) error {
	tw := newSectionWriter(out, "FILESYSTEMS", "MOUNT\tDEVICE\tTYPE\tUSED\tAVAILABLE\tUTILIZATION")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s / %s\t%s\t%s\n", row.Mountpoint, row.Device, row.FilesystemType, formatBytes(row.Used), formatBytes(row.Total), formatBytes(row.Available), formatPercent(row.Utilization))
	}
	return tw.Flush()
}

func renderDisks(out io.Writer, rows []hostDisk) error {
	tw := newSectionWriter(out, "DISKS", "DEVICE\tREAD\tWRITE\tI/O")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.Device, formatRate(row.ReadBytesPerSecond), formatRate(row.WriteBytesPerSecond), formatPercent(row.IOUtilization))
	}
	return tw.Flush()
}

func renderInterfaces(out io.Writer, rows []hostInterface) error {
	tw := newSectionWriter(out, "INTERFACES", "INTERFACE\tRECEIVE\tTRANSMIT\tUTILIZATION RX/TX\tERRORS RX/TX\tDROPPED RX/TX")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s / %s\t%s / %s\t%s / %s\n", row.Interface, formatRate(row.ReceiveBytesPerSecond), formatRate(row.TransmitBytesPerSecond), formatPercent(row.ReceiveUtilization), formatPercent(row.TransmitUtilization), formatNumber(row.ReceiveErrorsPerSecond), formatNumber(row.TransmitErrorsPerSecond), formatNumber(row.ReceiveDroppedPerSecond), formatNumber(row.TransmitDroppedPerSecond))
	}
	return tw.Flush()
}

func renderProcesses(out io.Writer, rows []hostProcess) error {
	tw := newSectionWriter(out, "PROCESSES", "PROCESS\tPID\tCPU\tRSS\tOPEN FDS")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", row.Process, displayText(row.PID), formatPercent(row.CPUPercent), formatBytes(row.MemoryRSS), formatNumber(row.OpenFileDescriptors))
	}
	return tw.Flush()
}

func newSectionWriter(out io.Writer, title, header string) *tabwriter.Writer {
	fmt.Fprintf(out, "\n%s\n", title)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	return tw
}

func displayText(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func formatLoad(load hostLoad) string {
	return fmt.Sprintf("%s / %s / %s", formatDecimal(load.One), formatDecimal(load.Five), formatDecimal(load.Fifteen))
}

func formatCapacitySummary(capacity hostCapacity) string {
	if capacity.Used == nil && capacity.Total == nil && capacity.Utilization == nil {
		return "—"
	}
	return fmt.Sprintf("%s / %s (%s)", formatBytes(capacity.Used), formatBytes(capacity.Total), formatPercent(capacity.Utilization))
}

func formatPercent(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *value)
}

func formatDecimal(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *value)
}

func formatNumber(value *float64) string {
	if value == nil {
		return "—"
	}
	if math.Trunc(*value) == *value {
		return fmt.Sprintf("%.0f", *value)
	}
	return fmt.Sprintf("%.2f", *value)
}

func formatBytes(value *float64) string {
	if value == nil {
		return "—"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	amount := *value
	unit := 0
	for math.Abs(amount) >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", amount, units[unit])
}

func formatRate(value *float64) string {
	if value == nil {
		return "—"
	}
	return formatBytes(value) + "/s"
}

func formatDuration(value *float64) string {
	if value == nil {
		return "—"
	}
	seconds := int64(*value)
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
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
