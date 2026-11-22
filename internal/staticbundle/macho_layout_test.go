package staticbundle

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Cross-assemble and link Intel Mach-O on either Mac architecture. No Intel
// executable is run under Rosetta: inspect the exact RIP-relative vector load.
func TestMachOSectionAnchorsPreserveAnonymousConstants(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple assembler/linker required")
	}
	d := t.TempDir()
	sources := map[string]string{
		"first": `.section __TEXT,__const
.globl _prefix
.weak_definition _prefix
_prefix:
.byte 1,2
.subsections_via_symbols
`,
		"vector": `.section __TEXT,__const
.p2align 5
Lvector:
.long 1,2,3,4,5,6,7,8
.section __DATA,__data
.quad _external
.text
.globl _read_vector
_read_vector:
vmovaps Lvector(%rip), %ymm0
ret
.globl _call_external
_call_external:
callq _external
ret
.subsections_via_symbols
`,
		"main": `.section __TEXT,__const
.p2align 6
.globl _prefix
.weak_definition _prefix
_prefix:
.space 100,170
.text
.globl _main
_main:
xorl %eax, %eax
ret
.globl _external
_external:
ret
.subsections_via_symbols
`,
		// Legacy indirect-pointer metadata is round-tripped, not linked: modern
		// Apple x86 linkers do not accept this section kind in input objects.
		"indirect": `.section __TEXT,__const
.long 42
.section __DATA,__nl_symbol_ptr,non_lazy_symbol_pointers
.indirect_symbol _external
.quad 0
`,
	}
	runTool := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = d
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, output)
		}
	}
	for name, source := range sources {
		put(t, filepath.Join(d, name+".s"), []byte(source))
		runTool("clang", "-target", "x86_64-apple-macos11", "-Wa,-L", "-c", name+".s", "-o", name+".o")
	}
	indirect, err := os.ReadFile(filepath.Join(d, "indirect.o"))
	if err != nil {
		t.Fatal(err)
	}
	anchoredIndirect, err := anchorMachOSections(indirect, "indirect")
	if err != nil {
		t.Fatal(err)
	}
	checkMachOAnchoredReferences(t, indirect, anchoredIndirect)
	want := make([]byte, 32)
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint32(want[4*i:], uint32(i+1))
	}
	for _, anchored := range []bool{false, true} {
		obj := "vector.o"
		if anchored {
			data, err := os.ReadFile(filepath.Join(d, obj))
			if err != nil {
				t.Fatal(err)
			}
			original := append([]byte(nil), data...)
			data, err = anchorMachOSections(data, "vector")
			if err != nil {
				t.Fatal(err)
			}
			checkMachOAnchoredReferences(t, original, data)
			obj = "anchored.o"
			put(t, filepath.Join(d, obj), data)
			// Reapplying must not add symbols or change bytes.
			again, err := anchorMachOSections(data, "vector")
			if err != nil || !bytes.Equal(data, again) {
				t.Fatalf("section anchoring is not idempotent: %v", err)
			}
		}
		runTool("ld", "-r", "-keep_private_externs", "-arch", "x86_64", "-platform_version", "macos", "11.0", "11.0", "first.o", obj, "-o", "shard.o")
		runTool("clang", "-target", "x86_64-apple-macos11", "main.o", "shard.o", "-o", "consumer")
		got, addr := vectorConstant(t, filepath.Join(d, "consumer"))
		if anchored && (!bytes.Equal(got, want) || addr%32 != 0) {
			t.Fatalf("anchored vector corrupted or unaligned at %#x: %x", addr, got)
		}
		if !anchored && bytes.Equal(got, want) {
			t.Log("linker preserved anonymous constants without anchors; fixed path still checked")
		}
	}
}

func checkMachOAnchoredReferences(t *testing.T, original, anchored []byte) {
	t.Helper()
	before, err := macho.NewFile(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	after, err := macho.NewFile(bytes.NewReader(anchored))
	if err != nil {
		t.Fatal(err)
	}
	if before.Flags != after.Flags || len(before.Sections) != len(after.Sections) {
		t.Fatal("anchoring changed Mach-O flags or sections")
	}
	for i, s := range before.Sections {
		a := after.Sections[i]
		oldData, _ := s.Data()
		newData, _ := a.Data()
		if s.SectionHeader != a.SectionHeader || !bytes.Equal(oldData, newData) || len(s.Relocs) != len(a.Relocs) {
			t.Fatal("anchoring changed section layout or contents")
		}
		for j, r := range s.Relocs {
			n := a.Relocs[j]
			if r.Extern {
				if before.Symtab.Syms[r.Value] != after.Symtab.Syms[n.Value] {
					t.Fatal("anchoring changed external relocation target")
				}
				n.Value = r.Value
			}
			if r != n {
				t.Fatal("anchoring changed relocation kind, address or addend")
			}
		}
	}
	for i, sym := range before.Dysymtab.IndirectSyms {
		if before.Symtab.Syms[sym] != after.Symtab.Syms[after.Dysymtab.IndirectSyms[i]] {
			t.Fatal("anchoring changed indirect symbol target")
		}
	}
}

func vectorConstant(t *testing.T, path string) ([]byte, uint64) {
	t.Helper()
	f, err := macho.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var pc uint64
	for _, s := range f.Symtab.Syms {
		if s.Name == "_read_vector" {
			pc = s.Value
		}
	}
	read := func(addr uint64, size int) []byte {
		for _, s := range f.Sections {
			if addr >= s.Addr && addr+uint64(size) <= s.Addr+s.Size {
				data, err := s.Data()
				if err != nil {
					t.Fatal(err)
				}
				return data[addr-s.Addr : addr-s.Addr+uint64(size)]
			}
		}
		return nil
	}
	code := read(pc, 8)
	if len(code) != 8 || !bytes.Equal(code[:4], []byte{0xc5, 0xfc, 0x28, 0x05}) {
		t.Fatalf("missing RIP-relative vmovaps at %#x: %x", pc, code)
	}
	addr := uint64(int64(pc+8) + int64(int32(binary.LittleEndian.Uint32(code[4:]))))
	return read(addr, 32), addr
}
