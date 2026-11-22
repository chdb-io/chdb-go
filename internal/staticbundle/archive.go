package staticbundle

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type member struct {
	name         string
	offset, size int64
}

// Archive members are addressed by offset, never extracted by name. Different
// build directories may contribute identically named .o files to one archive.
// Ordinary ar x would silently overwrite those files and lose engine code.
func readArchive(f *os.File) ([]member, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	magic := make([]byte, 8)
	if _, err := f.ReadAt(magic, 0); err != nil {
		return nil, err
	}
	if string(magic) != "!<arch>\n" {
		return nil, fmt.Errorf("expected a regular ar archive (thin archives are not portable)")
	}
	var result []member
	var names []byte
	for off := int64(8); off < st.Size(); {
		h := make([]byte, 60)
		if _, err := f.ReadAt(h, off); err != nil {
			return nil, fmt.Errorf("ar header at %d: %w", off, err)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
		if err != nil || n < 0 || string(h[58:60]) != "`\n" || n > st.Size()-off-60 {
			return nil, fmt.Errorf("invalid ar member at %d", off)
		}
		name := strings.TrimSpace(string(h[:16]))
		m := member{offset: off + 60, size: n}
		switch {
		case name == "/" || name == "/SYM64/":
			name = ""
		case name == "//":
			if n > 16<<20 {
				return nil, fmt.Errorf("oversized GNU ar name table")
			}
			names = make([]byte, n)
			if _, err := f.ReadAt(names, m.offset); err != nil {
				return nil, err
			}
		case strings.HasPrefix(name, "#1/"):
			nl, err := strconv.ParseInt(name[3:], 10, 64)
			if err != nil || nl < 1 || nl > n || nl > 1<<20 {
				return nil, fmt.Errorf("invalid BSD ar name at %d", off)
			}
			b := make([]byte, nl)
			if _, err := f.ReadAt(b, m.offset); err != nil {
				return nil, err
			}
			name = strings.TrimRight(string(b), "\x00")
			m.offset += nl
			m.size -= nl
		case strings.HasPrefix(name, "/") && name != "/" && name != "/SYM64/":
			i, err := strconv.Atoi(name[1:])
			if err != nil || i < 0 || i >= len(names) {
				return nil, fmt.Errorf("invalid GNU ar name reference %q", name)
			}
			end := strings.IndexByte(string(names[i:]), '\n')
			if end < 0 {
				return nil, fmt.Errorf("unterminated GNU ar name")
			}
			name = strings.TrimSuffix(string(names[i:i+end]), "/")
		default:
			name = strings.TrimSuffix(name, "/")
		}
		if name != "//" && name != "" && name != "/SYM64/" && !strings.HasPrefix(name, "__.SYMDEF") {
			m.name = name
			result = append(result, m)
		}
		off += 60 + n + n%2
		if off > st.Size() {
			return nil, fmt.Errorf("missing ar padding")
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("archive contains no objects")
	}
	return result, nil
}

// Validate every input and output as a real host object, not LLVM bitcode or a
// renamed archive. On Darwin retain the strictest deployment target of all
// input objects; never silently lower the library's minimum OS version.
func checkObject(r io.ReaderAt, platform string) (minimum, sdk uint32, err error) {
	if strings.HasPrefix(platform, "darwin-") {
		f, err := macho.NewFile(r)
		if err != nil {
			return 0, 0, err
		}
		want := macho.CpuArm64
		if platform == "darwin-amd64" {
			want = macho.CpuAmd64
		}
		if f.Type != macho.TypeObj || f.Cpu != want {
			return 0, 0, fmt.Errorf("expected %s relocatable object, got %s/%s", platform, f.Type, f.Cpu)
		}
		for _, l := range f.Loads {
			b := l.Raw()
			if len(b) < 16 {
				continue
			}
			switch f.ByteOrder.Uint32(b) {
			case 0x32: // LC_BUILD_VERSION
				if len(b) < 24 || f.ByteOrder.Uint32(b[8:]) != 1 {
					return 0, 0, fmt.Errorf("expected macOS LC_BUILD_VERSION")
				}
				minimum = maxVersion(minimum, f.ByteOrder.Uint32(b[12:]))
				sdk = maxVersion(sdk, f.ByteOrder.Uint32(b[16:]))
			case 0x24: // LC_VERSION_MIN_MACOSX
				minimum = maxVersion(minimum, f.ByteOrder.Uint32(b[8:]))
				sdk = maxVersion(sdk, f.ByteOrder.Uint32(b[12:]))
			}
		}
		return minimum, sdk, nil
	}
	f, err := elf.NewFile(r)
	if err != nil {
		return 0, 0, err
	}
	want := elf.EM_AARCH64
	if platform == "linux-amd64" {
		want = elf.EM_X86_64
	}
	if f.Type != elf.ET_REL || f.Machine != want || f.Class != elf.ELFCLASS64 || f.ByteOrder != binary.LittleEndian {
		return 0, 0, fmt.Errorf("expected %s ELF relocatable object", platform)
	}
	return 0, 0, nil
}

func maxVersion(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}
func osVersion(v uint32) string { return fmt.Sprintf("%d.%d.%d", v>>16, (v>>8)&255, v&255) }
