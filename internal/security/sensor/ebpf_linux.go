//go:build linux

package sensor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/features"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

type lpmKey struct {
	PrefixLen uint32
	Address   uint32
}

type linuxAttachment struct {
	policy  *ebpf.Map
	events  *ebpf.Map
	program *ebpf.Program
	link    link.Link
}

func linuxCapabilities() Capabilities {
	caps := Capabilities{Platform: "linux", CgroupV2: cgroupV2Mounted(), BTF: fileExists("/sys/kernel/btf/vmlinux")}
	caps.BPFSyscall = features.HaveMapType(ebpf.Array) == nil
	caps.RingBuffer = features.HaveMapType(ebpf.RingBuf) == nil
	caps.CgroupConnect = features.HaveProgramType(ebpf.CGroupSockAddr) == nil
	if data, err := os.ReadFile("/sys/kernel/security/lsm"); err == nil {
		for _, name := range splitCommaSpace(string(data)) {
			if name == "bpf" {
				caps.LSM = true
			}
		}
	}
	return caps
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func splitCommaSpace(value string) []string {
	var result []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func RunHelper(args []string) error {
	options, err := parseHelperArgs(args)
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("atcode-sensor must run as the privileged helper, not as the agent user")
	}
	if err := validateTargetPIDForOwner(options.TargetPID, options.OwnerUID); err != nil {
		return err
	}
	if options.CgroupPath == "" {
		options.CgroupPath = cgroupForPID(options.TargetPID)
	}
	if options.CgroupPath == "" {
		return errors.New("could not resolve a cgroup v2 scope for the target PID")
	}
	if err := ValidateHelperOptions(options.Options); err != nil {
		return err
	}
	caps := linuxCapabilities()
	status := Status{Version: statusVersion, State: StateStarting, Mode: ModeUnavailable, Capabilities: caps, TargetPID: options.TargetPID, OwnerUID: options.OwnerUID, Cgroup: options.CgroupPath, UpdatedAt: time.Now().UTC()}
	if err := writeStatus(options.Root, status, options.OwnerUID); err != nil {
		return err
	}
	if !caps.CgroupV2 || !caps.BPFSyscall || !caps.CgroupConnect {
		status.State = StateUnavailable
		status.LastError = "required cgroup v2, BPF syscall, or cgroup connect capability is unavailable"
		status.UpdatedAt = time.Now().UTC()
		_ = writeStatus(options.Root, status, options.OwnerUID)
		return errors.New(status.LastError)
	}
	attachment, err := attachCgroupConnect(options.Options)
	if err != nil {
		status.State = StateUnavailable
		status.LastError = boundedError(err)
		status.UpdatedAt = time.Now().UTC()
		_ = writeStatus(options.Root, status, options.OwnerUID)
		return err
	}
	defer attachment.Close()
	status.State = StateRunning
	status.HelperPID = os.Getpid()
	status.Mode = ModeDetectOnly
	if options.Enforce && len(options.DeniedCIDRs) > 0 {
		status.Mode = ModeEnforce
	}
	status.AttachedHooks = []string{"cgroup/connect4"}
	if !caps.RingBuffer {
		status.DegradedHooks = append(status.DegradedHooks, "ringbuf/events")
	}
	status.UpdatedAt = time.Now().UTC()
	if err := writeStatus(options.Root, status, options.OwnerUID); err != nil {
		return err
	}

	reader, err := ringbuf.NewReader(attachment.events)
	if err != nil {
		status.DegradedHooks = append(status.DegradedHooks, "userspace-event-reader")
		status.LastError = boundedError(err)
		_ = writeStatus(options.Root, status, options.OwnerUID)
		return waitForSignal()
	}
	defer reader.Close()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for {
			record, readErr := reader.Read()
			if readErr != nil {
				return
			}
			event, parseErr := parseKernelEvent(record.RawSample)
			if parseErr != nil {
				status.Dropped++
				continue
			}
			status.Events++
			if event.Verdict == "blocked" {
				status.Blocked++
			}
			status.UpdatedAt = time.Now().UTC()
			_ = writeStatus(options.Root, status, options.OwnerUID)
		}
	}()
	<-stop
	_ = reader.Close()
	<-eventsDone
	status.State = StateOff
	status.Mode = ModeOff
	status.HelperPID = 0
	status.UpdatedAt = time.Now().UTC()
	return writeStatus(options.Root, status, options.OwnerUID)
}

func validateTargetPIDForOwner(pid, ownerUID int) error {
	if err := validateTargetPID(pid); err != nil && os.Geteuid() != 0 {
		return err
	}
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return fmt.Errorf("sensor target PID %d is unavailable: %w", pid, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != ownerUID {
		return fmt.Errorf("sensor target PID %d is not owned by UID %d", pid, ownerUID)
	}
	return nil
}

func waitForSignal() error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	<-stop
	return nil
}

func (a *linuxAttachment) Close() {
	if a.link != nil {
		_ = a.link.Close()
	}
	if a.program != nil {
		_ = a.program.Close()
	}
	if a.events != nil {
		_ = a.events.Close()
	}
	if a.policy != nil {
		_ = a.policy.Close()
	}
}

func attachCgroupConnect(options Options) (*linuxAttachment, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove BPF memlock limit: %w", err)
	}
	policy, err := ebpf.NewMap(&ebpf.MapSpec{Name: "sonut_denied_ipv4", Type: ebpf.LPMTrie, KeySize: 8, ValueSize: 1, MaxEntries: maxDeniedCIDRs, Flags: 1})
	if err != nil {
		return nil, fmt.Errorf("create bounded policy map: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = policy.Close()
		}
	}()
	validated, err := ValidatePolicy(options.DeniedCIDRs, nil)
	if err != nil {
		return nil, err
	}
	for _, network := range validated.DeniedCIDRs {
		bits, _ := network.Mask.Size()
		if bits != 32 {
			return nil, fmt.Errorf("only IPv4 CIDRs are supported by the cgroup hook")
		}
		// Map keys use the host byte order produced by the cgroup context's
		// raw user_ip4 load. The bytes in memory remain the network-order IPv4
		// address while the Go integer is encoded natively by ebpf.Map.Put.
		address := binary.NativeEndian.Uint32(network.IP.To4())
		if err := policy.Put(lpmKey{PrefixLen: uint32(bits), Address: address}, uint8(1)); err != nil {
			return nil, fmt.Errorf("load denied CIDR %s: %w", network.String(), err)
		}
	}
	events, err := ebpf.NewMap(&ebpf.MapSpec{Name: "sonut_connect_events", Type: ebpf.RingBuf, MaxEntries: 1 << 20})
	if err != nil {
		return nil, fmt.Errorf("create bounded event ring buffer: %w", err)
	}
	program, err := ebpf.NewProgram(&ebpf.ProgramSpec{Name: "sonut_connect4", Type: ebpf.CGroupSockAddr, AttachType: ebpf.AttachCGroupInet4Connect, License: "GPL", Instructions: connectProgram(policy, events)})
	if err != nil {
		events.Close()
		return nil, fmt.Errorf("load reviewed cgroup program: %w", err)
	}
	cgroupLink, err := link.AttachCgroup(link.CgroupOptions{Path: options.CgroupPath, Attach: ebpf.AttachCGroupInet4Connect, Program: program})
	if err != nil {
		program.Close()
		events.Close()
		return nil, fmt.Errorf("attach cgroup connect4 hook: %w", err)
	}
	closeOnError = false
	return &linuxAttachment{policy: policy, events: events, program: program, link: cgroupLink}, nil
}

func connectProgram(policy, events *ebpf.Map) asm.Instructions {
	emit := func() asm.Instructions {
		return asm.Instructions{
			asm.LoadMapPtr(asm.R1, events.FD()),
			asm.Mov.Reg(asm.R2, asm.RFP), asm.Add.Imm(asm.R2, -16),
			asm.Mov.Imm(asm.R3, 16), asm.Mov.Imm(asm.R4, 0),
			asm.FnRingbufOutput.Call(),
		}
	}
	instructions := asm.Instructions{
		// bpf_sock_addr.user_ip4 is the u32 at offset 8. The stack record is
		// fixed-size and contains only destination IP, port placeholder, PID
		// placeholder, and the allow/block verdict.
		asm.StoreImm(asm.RFP, -16, 0, asm.DWord),
		asm.LoadMem(asm.R2, asm.R1, 8, asm.Word),
		asm.StoreMem(asm.RFP, -16, asm.R2, asm.Word),
		// user_port is a u32 at offset 48 in bpf_sock_addr. It is retained
		// as a bounded event field; the deny map intentionally matches only
		// resolved IPv4 CIDRs inside the kernel.
		asm.LoadMem(asm.R3, asm.R1, 48, asm.Word),
		asm.StoreMem(asm.RFP, -12, asm.R3, asm.Word),
		asm.StoreImm(asm.RFP, -8, 0, asm.DWord),
		asm.StoreImm(asm.RFP, -4, 0, asm.DWord),
		asm.StoreImm(asm.RFP, -32, 32, asm.Word),
		asm.StoreMem(asm.RFP, -28, asm.R2, asm.Word),
		asm.LoadMapPtr(asm.R1, policy.FD()),
		asm.Mov.Reg(asm.R2, asm.RFP), asm.Add.Imm(asm.R2, -32),
		asm.FnMapLookupElem.Call(),
		asm.JEq.Imm(asm.R0, 0, "allow"),
		asm.StoreImm(asm.RFP, -4, 1, asm.DWord),
	}
	// The two branches are appended separately so each branch has a stable
	// return verdict after emitting its bounded, payload-free event. The
	// assembler resolves the allow label and cilium/ebpf validates the program.
	instructions = append(instructions, emit()...)
	instructions = append(instructions, asm.Mov.Imm(asm.R0, 0), asm.Return())
	instructions = append(instructions, asm.StoreImm(asm.RFP, -4, 0, asm.DWord).WithSymbol("allow"))
	instructions = append(instructions, emit()...)
	instructions = append(instructions, asm.Mov.Imm(asm.R0, 1), asm.Return())
	return instructions
}

func cgroupForPID(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[1] == "" {
			relative := strings.TrimPrefix(parts[2], "/")
			return filepath.Join("/sys/fs/cgroup", relative)
		}
	}
	return ""
}

type helperOptions struct {
	Options
	OwnerUID int
}

func parseHelperArgs(args []string) (helperOptions, error) {
	options := helperOptions{Options: Options{Enforce: false}, OwnerUID: -1}
	for index := 0; index < len(args); index++ {
		read := func() (string, error) {
			index++
			if index >= len(args) {
				return "", errors.New("missing value for " + args[index-1])
			}
			return args[index], nil
		}
		switch args[index] {
		case "--root":
			value, err := read()
			if err != nil {
				return options, err
			}
			options.Root = value
		case "--pid":
			value, err := read()
			if err != nil {
				return options, err
			}
			options.TargetPID, err = strconv.Atoi(value)
			if err != nil {
				return options, fmt.Errorf("invalid target PID: %w", err)
			}
		case "--owner-uid":
			value, err := read()
			if err != nil {
				return options, err
			}
			options.OwnerUID, err = strconv.Atoi(value)
			if err != nil {
				return options, fmt.Errorf("invalid owner UID: %w", err)
			}
		case "--cgroup":
			value, err := read()
			if err != nil {
				return options, err
			}
			options.CgroupPath = value
		case "--deny-cidr":
			value, err := read()
			if err != nil {
				return options, err
			}
			options.DeniedCIDRs = append(options.DeniedCIDRs, value)
		case "--deny-port":
			value, err := read()
			if err != nil {
				return options, err
			}
			port, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				return options, parseErr
			}
			options.DeniedPorts = append(options.DeniedPorts, port)
		case "--enforce":
			options.Enforce = true
		default:
			return options, fmt.Errorf("unknown helper argument %q", args[index])
		}
	}
	if options.Root == "" || options.TargetPID <= 0 || options.OwnerUID < 0 {
		return options, errors.New("helper requires --root, positive --pid, and --owner-uid")
	}
	return options, nil
}
