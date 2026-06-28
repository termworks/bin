// Package elfpatch is a small, pure-Go reimplementation of the parts of patchelf
// bin needs: changing an ELF's interpreter and its RPATH/RUNPATH — without
// shelling out.
//
// Setting the interpreter supports both the in-place case and the "grow" case
// (a longer path), because the Linux kernel reads PT_INTERP directly from the
// file offset, so a longer interpreter can be appended at EOF and PT_INTERP
// re-pointed at it without relocating program headers.
//
// Setting RPATH/RUNPATH is in-place when the new value fits the existing string;
// otherwise it grows the binary by appending a fresh .dynstr (old strings +
// the new rpath) and mapping it through a repurposed PT_NOTE -> PT_LOAD segment,
// then repointing DT_STRTAB/DT_STRSZ and a DT_RUNPATH entry at it.
package elfpatch

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
)

// ErrNeedsGrow is returned when an operation can't be done in place and the
// (not-yet-implemented) relocation path would be required.
var ErrNeedsGrow = fmt.Errorf("value longer than existing slot (segment relocation required)")

// elfImage is a mutable in-memory ELF64 little-endian image.
type elfImage struct {
	data      []byte
	mode      os.FileMode
	phoff     uint64
	phentsize int
	phnum     int
	shoff     uint64
	shentsize int
	shnum     int
}

func load(path string) (*elfImage, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 64 || !bytes.Equal(data[:4], []byte("\x7fELF")) {
		return nil, fmt.Errorf("not an ELF file")
	}
	if data[4] != 2 { // EI_CLASS: 2 = ELFCLASS64
		return nil, fmt.Errorf("only 64-bit ELF is supported")
	}
	if data[5] != 1 { // EI_DATA: 1 = little-endian
		return nil, fmt.Errorf("only little-endian ELF is supported")
	}
	img := &elfImage{
		data:      data,
		mode:      fi.Mode(),
		phoff:     binary.LittleEndian.Uint64(data[0x20:]),
		phentsize: int(binary.LittleEndian.Uint16(data[0x36:])),
		phnum:     int(binary.LittleEndian.Uint16(data[0x38:])),
		shoff:     binary.LittleEndian.Uint64(data[0x28:]),
		shentsize: int(binary.LittleEndian.Uint16(data[0x3a:])),
		shnum:     int(binary.LittleEndian.Uint16(data[0x3c:])),
	}
	if img.phoff == 0 || img.phentsize < 56 {
		return nil, fmt.Errorf("missing or malformed program header table")
	}
	return img, nil
}

func (img *elfImage) write(path string) error {
	return os.WriteFile(path, img.data, img.mode.Perm())
}

// field offsets within a 64-bit program header entry.
const (
	pType   = 0
	pFlags  = 4
	pOffset = 8
	pVaddr  = 16
	pPaddr  = 24
	pFilesz = 32
	pMemsz  = 40
	pAlign  = 48
)

// field offsets within a 64-bit section header entry.
const (
	shAddr   = 16
	shOffset = 24
	shSize   = 32
)

func (img *elfImage) progOff(i int) int { return int(img.phoff) + i*img.phentsize }

func (img *elfImage) progU64(i, field int) uint64 {
	return binary.LittleEndian.Uint64(img.data[img.progOff(i)+field:])
}
func (img *elfImage) progU32(i, field int) uint32 {
	return binary.LittleEndian.Uint32(img.data[img.progOff(i)+field:])
}
func (img *elfImage) setProgU64(i, field int, v uint64) {
	binary.LittleEndian.PutUint64(img.data[img.progOff(i)+field:], v)
}
func (img *elfImage) setProgU32(i, field int, v uint32) {
	binary.LittleEndian.PutUint32(img.data[img.progOff(i)+field:], v)
}

// findProg returns the index of the first program header of the given type, or -1.
func (img *elfImage) findProg(t elf.ProgType) int {
	for i := 0; i < img.phnum; i++ {
		if elf.ProgType(img.progU32(i, pType)) == t {
			return i
		}
	}
	return -1
}

// vaddrToOff maps a virtual address into a file offset via the PT_LOAD segments.
func (img *elfImage) vaddrToOff(v uint64) (uint64, bool) {
	for i := 0; i < img.phnum; i++ {
		if elf.ProgType(img.progU32(i, pType)) != elf.PT_LOAD {
			continue
		}
		va := img.progU64(i, pVaddr)
		fs := img.progU64(i, pFilesz)
		if v >= va && v < va+fs {
			return img.progU64(i, pOffset) + (v - va), true
		}
	}
	return 0, false
}

// maxLoadVaddrEnd returns the highest mapped virtual address end across PT_LOADs.
func (img *elfImage) maxLoadVaddrEnd() uint64 {
	var end uint64
	for i := 0; i < img.phnum; i++ {
		if elf.ProgType(img.progU32(i, pType)) != elf.PT_LOAD {
			continue
		}
		if e := img.progU64(i, pVaddr) + img.progU64(i, pMemsz); e > end {
			end = e
		}
	}
	return end
}

// setSectionByAddr updates the offset/size/addr of the section whose sh_addr
// matches addr (used to keep the .dynstr section view consistent after a move).
func (img *elfImage) setSectionByAddr(addr, newOff, newSize, newAddr uint64) {
	if img.shoff == 0 {
		return
	}
	for i := 0; i < img.shnum; i++ {
		base := int(img.shoff) + i*img.shentsize
		if base+shSize+8 > len(img.data) {
			return
		}
		if binary.LittleEndian.Uint64(img.data[base+shAddr:]) == addr {
			binary.LittleEndian.PutUint64(img.data[base+shOffset:], newOff)
			binary.LittleEndian.PutUint64(img.data[base+shSize:], newSize)
			binary.LittleEndian.PutUint64(img.data[base+shAddr:], newAddr)
			return
		}
	}
}

// SetInterpreter sets the ELF interpreter, in place when the new path fits the
// existing PT_INTERP slot, otherwise by appending it at EOF and re-pointing
// PT_INTERP (which the kernel reads straight from the file offset).
func SetInterpreter(path, interp string) error {
	img, err := load(path)
	if err != nil {
		return err
	}
	i := img.findProg(elf.PT_INTERP)
	if i < 0 {
		return fmt.Errorf("no PT_INTERP segment (statically linked?)")
	}

	val := append([]byte(interp), 0)
	off := img.progU64(i, pOffset)
	size := img.progU64(i, pFilesz)

	if uint64(len(val)) <= size {
		// in place: overwrite, NUL-pad the remainder of the slot
		buf := make([]byte, size)
		copy(buf, val)
		copy(img.data[off:off+size], buf)
	} else {
		// grow: append at EOF and re-point PT_INTERP
		newOff := uint64(len(img.data))
		img.data = append(img.data, val...)
		img.setProgU64(i, pOffset, newOff)
		img.setProgU64(i, pFilesz, uint64(len(val)))
		img.setProgU64(i, pMemsz, uint64(len(val)))
	}
	return img.write(path)
}

// Interpreter returns the binary's current interpreter path.
func Interpreter(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			b := make([]byte, p.Filesz)
			if _, err := p.ReadAt(b, 0); err != nil {
				return "", err
			}
			return string(bytes.TrimRight(b, "\x00")), nil
		}
	}
	return "", fmt.Errorf("no interpreter")
}

// Runpath returns the binary's RUNPATH (preferred) or RPATH entries.
func Runpath(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if rp, _ := f.DynString(elf.DT_RUNPATH); len(rp) > 0 {
		return rp, nil
	}
	return f.DynString(elf.DT_RPATH)
}

// SetRunpath sets the binary's DT_RUNPATH. It overwrites an existing
// RUNPATH/RPATH in place when the new value fits; otherwise (longer value, or
// none present) it grows the binary by appending a new .dynstr and mapping it
// via a repurposed PT_NOTE segment.
func SetRunpath(path, rpath string) error {
	err := setRunpathInPlace(path, rpath)
	if err == nil || !errorsIsGrow(err) {
		return err
	}
	return setRunpathGrow(path, rpath)
}

func errorsIsGrow(err error) bool {
	for err != nil {
		if err == ErrNeedsGrow {
			return true
		}
		type wrapped interface{ Unwrap() error }
		w, ok := err.(wrapped)
		if !ok {
			return false
		}
		err = w.Unwrap()
	}
	return false
}

func setRunpathInPlace(path, rpath string) error {
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	cur, _ := f.DynString(elf.DT_RUNPATH)
	if len(cur) == 0 {
		cur, _ = f.DynString(elf.DT_RPATH)
	}
	dynstr := f.Section(".dynstr")
	if len(cur) == 0 || dynstr == nil {
		f.Close()
		return fmt.Errorf("no existing RUNPATH/RPATH to edit: %w", ErrNeedsGrow)
	}
	data, err := dynstr.Data()
	if err != nil {
		f.Close()
		return err
	}
	old := cur[0]
	strOff := dynstr.Offset
	f.Close()

	if len(rpath) > len(old) {
		return fmt.Errorf("rpath longer than slot: %w", ErrNeedsGrow)
	}
	idx := bytes.Index(data, append([]byte(old), 0))
	if idx < 0 {
		return fmt.Errorf("could not locate current rpath in .dynstr")
	}
	fh, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer fh.Close()
	buf := make([]byte, len(old)+1) // NUL-padded
	copy(buf, rpath)
	_, err = fh.WriteAt(buf, int64(strOff)+int64(idx))
	return err
}

// dynamic-section tag values (subset).
const dynEntSize = 16

// setRunpathGrow appends a new .dynstr containing the rpath, maps it via a
// repurposed PT_NOTE -> PT_LOAD segment, and repoints DT_STRTAB/DT_STRSZ and a
// DT_RUNPATH entry (reusing DT_RPATH or a spare DT_NULL slot) at it.
func setRunpathGrow(path, rpath string) error {
	img, err := load(path)
	if err != nil {
		return err
	}

	di := img.findProg(elf.PT_DYNAMIC)
	if di < 0 {
		return fmt.Errorf("no PT_DYNAMIC segment")
	}
	dynOff := int(img.progU64(di, pOffset))
	nEnt := int(img.progU64(di, pFilesz)) / dynEntSize

	idxStrtab, idxStrsz, idxRunpath, idxRpath, idxNull := -1, -1, -1, -1, -1
	var strtabVaddr, strsz uint64
	for k := 0; k < nEnt; k++ {
		base := dynOff + k*dynEntSize
		tag := elf.DynTag(binary.LittleEndian.Uint64(img.data[base:]))
		val := binary.LittleEndian.Uint64(img.data[base+8:])
		switch tag {
		case elf.DT_NULL:
			if idxNull < 0 {
				idxNull = k
			}
		case elf.DT_STRTAB:
			idxStrtab, strtabVaddr = k, val
		case elf.DT_STRSZ:
			idxStrsz, strsz = k, val
		case elf.DT_RUNPATH:
			idxRunpath = k
		case elf.DT_RPATH:
			idxRpath = k
		}
	}
	if idxStrtab < 0 || idxStrsz < 0 {
		return fmt.Errorf("missing DT_STRTAB/DT_STRSZ")
	}

	strFileOff, ok := img.vaddrToOff(strtabVaddr)
	if !ok || strFileOff+strsz > uint64(len(img.data)) {
		return fmt.Errorf("cannot locate existing .dynstr")
	}
	old := img.data[strFileOff : strFileOff+strsz]

	newStr := make([]byte, 0, int(strsz)+len(rpath)+1)
	newStr = append(newStr, old...)
	rpathOff := uint64(len(newStr))
	newStr = append(newStr, rpath...)
	newStr = append(newStr, 0)

	ni := img.findProg(elf.PT_NOTE)
	if ni < 0 {
		return fmt.Errorf("no PT_NOTE segment to repurpose: %w", ErrNeedsGrow)
	}

	const align = uint64(0x1000)
	fileOff := uint64(len(img.data))
	base := (img.maxLoadVaddrEnd() + align - 1) / align * align
	vaddr := base + (fileOff % align)

	// append the new string table
	img.data = append(img.data, newStr...)

	// repurpose PT_NOTE as a read-only PT_LOAD covering the new .dynstr
	img.setProgU32(ni, pType, uint32(elf.PT_LOAD))
	img.setProgU32(ni, pFlags, uint32(elf.PF_R))
	img.setProgU64(ni, pOffset, fileOff)
	img.setProgU64(ni, pVaddr, vaddr)
	img.setProgU64(ni, pPaddr, vaddr)
	img.setProgU64(ni, pFilesz, uint64(len(newStr)))
	img.setProgU64(ni, pMemsz, uint64(len(newStr)))
	img.setProgU64(ni, pAlign, align)

	// repoint DT_STRTAB/DT_STRSZ and set DT_RUNPATH
	setVal := func(k int, v uint64) {
		binary.LittleEndian.PutUint64(img.data[dynOff+k*dynEntSize+8:], v)
	}
	setTag := func(k int, t elf.DynTag) {
		binary.LittleEndian.PutUint64(img.data[dynOff+k*dynEntSize:], uint64(t))
	}
	setVal(idxStrtab, vaddr)
	setVal(idxStrsz, uint64(len(newStr)))
	switch {
	case idxRunpath >= 0:
		setVal(idxRunpath, rpathOff)
	case idxRpath >= 0:
		setTag(idxRpath, elf.DT_RUNPATH)
		setVal(idxRpath, rpathOff)
	case idxNull >= 0 && idxNull < nEnt-1: // keep a terminating DT_NULL after it
		setTag(idxNull, elf.DT_RUNPATH)
		setVal(idxNull, rpathOff)
	default:
		return fmt.Errorf("no spare dynamic slot for DT_RUNPATH: %w", ErrNeedsGrow)
	}

	// keep the .dynstr section header consistent with its new home
	img.setSectionByAddr(strtabVaddr, fileOff, uint64(len(newStr)), vaddr)

	return img.write(path)
}
