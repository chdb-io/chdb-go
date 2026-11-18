package staticbundle

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"strings"
)

// anchorMachOSections gives anonymous section-leading data a local symbol before
// ld -r merges sections. Otherwise the leading constants of the next object can
// become the tail of a preceding weak atom. A later link may coalesce that atom
// with a different definition, silently replacing the constants (and their SIMD
// alignment). Keep MH_SUBSECTIONS_VIA_SYMBOLS: disabling it breaks weak-symbol
// coalescing across shards. The anchors are local, deterministic, and not stripped
// as assembler-temporary L* labels would be.
func anchorMachOSections(data []byte, objectID string) ([]byte, error) {
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if f.Magic != macho.Magic64 || f.Type != macho.TypeObj || f.ByteOrder != binary.LittleEndian || f.Symtab == nil {
		return nil, fmt.Errorf("expected a little-endian 64-bit Mach-O object with symbols")
	}
	var sections []int
	for i, s := range f.Sections {
		// Literal pools already have explicit atom boundaries. Do not add labels
		// to unwind metadata or instruction sections, which have their own rules.
		if s.Size == 0 || s.Flags&0xff != 0 || s.Flags&0x80000400 != 0 || s.Name == "__eh_frame" || s.Name == "__compact_unwind" {
			continue
		}
		named := false
		for _, sym := range f.Symtab.Syms {
			if sym.Type&1 == 0 && strings.HasPrefix(sym.Name, "L") {
				continue // ld strips assembler-temporary labels even if as kept them.
			}
			if sym.Type&0xe0 == 0 && sym.Type&0x0e == 0x0e && int(sym.Sect) == i+1 && sym.Value == s.Addr {
				named = true
				break
			}
		}
		if !named {
			sections = append(sections, i)
		}
	}
	if len(sections) == 0 {
		return data, nil
	}
	if len(f.Sections) > 255 || len(f.Symtab.Syms)+len(sections) >= 1<<24 {
		return nil, fmt.Errorf("Mach-O section or symbol index exceeds relocation limits")
	}
	order := f.ByteOrder
	// debug/macho exposes parsed symbols but does not populate SymtabCmd.
	var symtab macho.SymtabCmd
	if err := binary.Read(bytes.NewReader(f.Symtab.Raw()), order, &symtab); err != nil {
		return nil, err
	}
	var symcmd, dysymcmd int
	for off, i := 32, 0; i < int(f.Ncmd); i++ {
		cmd, size := order.Uint32(data[off:]), int(order.Uint32(data[off+4:]))
		if cmd == uint32(macho.LoadCmdSymtab) {
			symcmd = off
		}
		if cmd == uint32(macho.LoadCmdDysymtab) {
			dysymcmd = off
		}
		off += size
	}
	if symcmd == 0 {
		return nil, fmt.Errorf("missing Mach-O symbol table command")
	}
	insert := uint32(0)
	if d := f.Dysymtab; d != nil {
		if d.Ilocalsym != 0 || d.Iextdefsym != d.Nlocalsym || d.Iundefsym != d.Nlocalsym+d.Nextdefsym || d.Iundefsym+d.Nundefsym != symtab.Nsyms || d.Ntoc != 0 || d.Nmodtab != 0 || d.Nextrefsyms != 0 || d.Nextrel != 0 || d.Nlocrel != 0 {
			return nil, fmt.Errorf("unsupported Mach-O dynamic symbol table layout")
		}
		insert = d.Nlocalsym
	}
	count := uint32(len(sections))
	// Rebuild only the symbol/string tables at EOF. Section contents, addresses,
	// alignment, and relocation locations stay intact. Inserting locals before
	// external symbols requires rebasing symbol indices, not relocation addends.
	strtab := append([]byte(nil), data[symtab.Stroff:symtab.Stroff+symtab.Strsize]...)
	var anchors []byte
	for _, i := range sections {
		entry := make([]byte, 16)
		order.PutUint32(entry, uint32(len(strtab)))
		entry[4], entry[5] = 0x0e, byte(i+1) // local N_SECT
		order.PutUint64(entry[8:], f.Sections[i].Addr)
		anchors = append(anchors, entry...)
		strtab = append(strtab, fmt.Sprintf("l_chdb_section_%s_%d\x00", objectID, i)...)
	}
	table := data[symtab.Symoff : symtab.Symoff+16*symtab.Nsyms]
	result := append([]byte(nil), data...)
	for len(result)%8 != 0 {
		result = append(result, 0)
	}
	symoff := len(result)
	result = append(result, table[:16*insert]...)
	result = append(result, anchors...)
	result = append(result, table[16*insert:]...)
	stroff := len(result)
	result = append(result, strtab...)
	if uint64(len(result)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("Mach-O object exceeds 32-bit file offsets")
	}
	order.PutUint32(result[symcmd+8:], uint32(symoff))
	order.PutUint32(result[symcmd+12:], symtab.Nsyms+count)
	order.PutUint32(result[symcmd+16:], uint32(stroff))
	order.PutUint32(result[symcmd+20:], uint32(len(strtab)))
	if dysymcmd != 0 {
		for _, off := range []int{12, 16, 24} { // nlocalsym, iextdefsym, iundefsym
			order.PutUint32(result[dysymcmd+off:], order.Uint32(result[dysymcmd+off:])+count)
		}
		for i, sym := range f.Dysymtab.IndirectSyms {
			if sym&0xc0000000 == 0 && sym >= insert {
				order.PutUint32(result[int(f.Dysymtab.Indirectsymoff)+4*i:], sym+count)
			}
		}
	}
	for _, s := range f.Sections {
		for i, rel := range s.Relocs {
			if rel.Scattered {
				return nil, fmt.Errorf("unexpected scattered 64-bit Mach-O relocation")
			}
			if rel.Extern && rel.Value >= insert {
				off := int(s.Reloff) + 8*i + 4
				info := order.Uint32(result[off:])
				order.PutUint32(result[off:], info&0xff000000|(rel.Value+count))
			}
		}
	}
	return result, nil
}
