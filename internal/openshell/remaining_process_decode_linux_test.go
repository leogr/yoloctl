package openshell

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func nativeCensusStatFixture(tid int, state, start string) []byte {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[19] = state, start
	return []byte(fmt.Sprintf("%d (a ) name\nwith spaces) %s\n", tid, strings.Join(fields, " ")))
}

func nativeCensusStatusFixture(pid, tid int, kernel bool, nested bool) []byte {
	kthread := 0
	if kernel {
		kthread = 1
	}
	nspid := fmt.Sprint(tid)
	if nested {
		nspid += " 1"
	}
	return []byte(fmt.Sprintf("Name:\tfixture\nTgid:\t%d\nPid:\t%d\nKthread:\t%d\nNSpid:\t%s\nUid:\t1000 1000 1000 1000\n", pid, tid, kthread, nspid))
}

func TestNativeCensusStatKeepsZombieAndZeroWithoutWeakeningActivation(t *testing.T) {
	for _, state := range []string{"R", "S", "D", "T", "t", "P", "I", "Z"} {
		for _, start := range []string{"0", "1", "18446744073709551615"} {
			raw := nativeCensusStatFixture(7, state, start)
			got, err := decodeNativeProcessStat(raw, 7)
			if err != nil || got.tid != 7 || got.start != start || got.zombie != (state == "Z") {
				t.Fatalf("lost explicit census identity/state: %v %v", got, err)
			}
			if state == "Z" || start == "0" {
				if _, err := processStartIdentity(raw, 7); err == nil {
					t.Fatal("existing live activation contract was weakened")
				}
			}
		}
	}
}

func TestNativeCensusStatRejectsUnknownDeadAndReusedIdentities(t *testing.T) {
	for _, raw := range [][]byte{
		nil, bytesTrimCensusFinalNewline(nativeCensusStatFixture(7, "S", "5")), nativeCensusStatFixture(8, "S", "5"), nativeCensusStatFixture(7, "X", "5"),
		nativeCensusStatFixture(7, "x", "5"), nativeCensusStatFixture(7, "?", "5"),
		nativeCensusStatFixture(7, "SS", "5"), nativeCensusStatFixture(7, "S", "00"),
		nativeCensusStatFixture(7, "S", "-1"), nativeCensusStatFixture(7, "S", "18446744073709551616"),
		[]byte("7 (fixture) S 0\n"), []byte("7 fixture) S 0\n"),
		append(nativeCensusStatFixture(7, "S", "5"), 0), []byte(strings.Repeat("x", nativeProcessMetadataLimit+1)),
	} {
		if _, err := decodeNativeProcessStat(raw, 7); err == nil {
			t.Fatalf("unsafe census stat accepted: %.100q", raw)
		}
	}
}

func TestNativeCensusStatusRetainsNonleaderKernelAndNestedTasks(t *testing.T) {
	for _, kernel := range []bool{true, false} {
		for _, nested := range []bool{true, false} {
			got, err := decodeNativeProcessStatus(nativeCensusStatusFixture(7, 8, kernel, nested), 7, 8)
			want := []int{8}
			if nested {
				want = append(want, 1)
			}
			if err != nil || got.kernelThread != kernel || !reflect.DeepEqual(got.nspids, want) {
				t.Fatalf("nonleader/kernel/nested task omitted: %v %v", got, err)
			}
		}
	}
}

func TestNativeCensusStatusRejectsMissingDuplicateAndForeignMembership(t *testing.T) {
	original := string(nativeCensusStatusFixture(7, 8, false, false))
	for _, raw := range []string{
		"", strings.TrimSuffix(original, "\n"), strings.Replace(original, "Tgid:\t7", "Tgid:\t9", 1),
		strings.Replace(original, "Pid:\t8", "Pid:\t9", 1),
		strings.Replace(original, "Kthread:\t0\n", "", 1),
		strings.Replace(original, "Kthread:\t0", "Kthread:\t2", 1),
		strings.Replace(original, "NSpid:\t8", "NSpid:\t9", 1),
		strings.Replace(original, "NSpid:\t8", "NSpid:\t8 0", 1),
		strings.Replace(original, "NSpid:\t8", "NSpid:\t08", 1),
		strings.Replace(original, "NSpid:\t8", "NSpid:\t8 "+strings.Repeat("1 ", 33), 1),
		original + "Tgid:\t7\n", original + "Kthread:\t0\n", original + "NSpid:\t8\n",
		original + "\x00", strings.Repeat("x", nativeProcessMetadataLimit+1),
	} {
		if _, err := decodeNativeProcessStatus([]byte(raw), 7, 8); err == nil {
			t.Fatalf("unsafe census status accepted: %.120q", raw)
		}
	}
}

func TestNativeCensusStatusRetainsAllNamespacePIDLevels(t *testing.T) {
	var values []string
	for n := 33; n >= 1; n-- {
		values = append(values, strconv.Itoa(n))
	}
	raw := strings.Replace(string(nativeCensusStatusFixture(33, 33, false, false)), "NSpid:\t33", "NSpid:\t"+strings.Join(values, " "), 1)
	got, err := decodeNativeProcessStatus([]byte(raw), 33, 33)
	if err != nil || len(got.nspids) != 33 {
		t.Fatalf("valid maximum-depth namespace observation refused: %+v %v", got, err)
	}
	for i, actual := range got.nspids {
		if actual != 33-i {
			t.Fatalf("namespace PID order changed at %d: %v", i, got.nspids)
		}
	}
	tooDeep := strings.Replace(raw, "NSpid:\t", "NSpid:\t33 ", 1)
	if _, err := decodeNativeProcessStatus([]byte(tooDeep), 33, 33); err == nil {
		t.Fatal("34 namespace PID entries accepted")
	}
}

func TestNativeCensusProcessNumbersNeverTurnInvalidNumericNamesIntoAbsence(t *testing.T) {
	for _, name := range []string{"0", "00", "01", "2147483648", "9999999999999999999999999"} {
		if _, _, err := nativeProcessNumber(name); err == nil {
			t.Fatalf("invalid numeric process name was ignored: %q", name)
		}
	}
	for _, name := range []string{"self", "thread-self", "mounts"} {
		if _, numeric, err := nativeProcessNumber(name); err != nil || numeric {
			t.Fatalf("nonprocess kernel entry misclassified: %q %v", name, err)
		}
	}
	if n, numeric, err := nativeProcessNumber("2147483647"); err != nil || !numeric || n != 2147483647 {
		t.Fatal(n, numeric, err)
	}
}

func nativeCensusMountFixture() ([]byte, pinFileIdentity) {
	return []byte("3 2 0:5 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw\n"), pinFileIdentity{device: unix.Mkdev(0, 5), inode: 1, mount: 3}
}

func TestNativeCensusProcMountRequiresUnfilteredExactMapping(t *testing.T) {
	raw, identity := nativeCensusMountFixture()
	for _, suffix := range []string{"rw", "ro", "rw,hidepid=0", "rw,hidepid=off,gid=1000"} {
		input := strings.TrimSuffix(string(raw), "rw\n") + suffix + "\n"
		got, err := decodeNativeProcessProcMount([]byte(input), identity)
		if err != nil || got.identity != identity || got.point != "/proc" || got.row == "" {
			t.Fatalf("complete unfiltered view refused: %v %v", got, err)
		}
	}
	// Ordinary non-task submounts do not remove task visibility.
	withNonTaskMount := append(append([]byte(nil), raw...), []byte("4 3 0:6 / /proc/sys/fs/binfmt_misc rw - binfmt_misc binfmt_misc rw\n")...)
	if _, err := decodeNativeProcessProcMount(withNonTaskMount, identity); err != nil {
		t.Fatal("unrelated non-task submount refused", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,hidepid=1", 1),
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,hidepid=2", 1),
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,hidepid=ptraceable", 1),
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,hidepid=0,hidepid=off", 1),
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,subset=pid", 1),
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,unknown=1", 1),
		strings.Replace(string(raw), "proc proc rw", "proc proc rw,gid=01", 1),
		strings.Replace(string(raw), " - proc", " mystery:1 - proc", 1),
		strings.Replace(string(raw), " / /proc", " /part /proc", 1),
		strings.Replace(string(raw), " - proc ", " - sysfs ", 1),
		string(raw) + "4 2 0:6 / /proc rw - proc proc rw\n",
		string(raw) + "4 3 0:6 / /proc/7/task/8/stat rw - tmpfs tmpfs rw\n",
		string(raw) + "4 3 0:6 / /proc/thread-self rw - proc proc rw\n",
		string(raw) + "4 3 0:6 / /proc/self/status rw - tmpfs tmpfs rw\n",
	} {
		if _, err := decodeNativeProcessProcMount([]byte(bad), identity); err == nil {
			t.Fatalf("filtered/unknown/obscured proc view accepted: %q", bad)
		}
	}
	for _, bad := range []pinFileIdentity{{}, {device: identity.device, inode: 1, mount: 8}, {device: unix.Mkdev(0, 8), inode: 1, mount: 3}} {
		if _, err := decodeNativeProcessProcMount(raw, bad); err == nil {
			t.Fatal("foreign proc descriptor mapping accepted", bad)
		}
	}
}

func bytesTrimCensusFinalNewline(raw []byte) []byte { return raw[:len(raw)-1] }
