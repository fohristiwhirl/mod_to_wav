package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

type Sample struct {
	Name			string
	Finetune		int
	Volume			int
	RepOffset		int
	RepLength		int
	Data			[]byte
}

func (self *Sample) String() string {
	return fmt.Sprintf("%22v (%5v bytes) - ft %v, v %v, rep %v %v", self.Name, len(self.Data), self.Finetune, self.Volume, self.RepOffset, self.RepLength)
}

type Pattern struct {
	Lines			[][]*Note
}

type Note struct {
	Sample			int
	Period			int				// This determines the pitch, I think
	Effect			int
	Parameter		int
}

type Modfile struct {
	Title			string
	Format			string
	ChannelCount	int
	SampleCount		int
	Table			[]int
	Samples			[]*Sample
	Patterns		[]*Pattern
	Unread			int
}

func (self *Modfile) Print() {

	fmt.Printf("\n")
	fmt.Printf("Title: \"%v\" (format: %s)\n", self.Title, self.Format)
	fmt.Printf("\n")

	sample_length_sum := 0

	for n := 1; n < len(self.Samples); n++ {
		fmt.Printf("%v\n", self.Samples[n])
		sample_length_sum += len(self.Samples[n].Data)
	}

	fmt.Printf("\n")
	fmt.Printf("%v bytes of sample data\n", sample_length_sum)

	fmt.Printf("Table:")
	for n := 0; n < len(self.Table); n++ {
		fmt.Printf(" %v", self.Table[n])
	}
	fmt.Printf("\n")

	fmt.Printf("%v unread bytes\n", self.Unread)
	fmt.Printf("\n")
}

func main() {

	if len(os.Args) < 2 {
		return
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	modfile, err := loadmod(f)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	modfile.Print()

}


func get_format(f *os.File) (format string, channels int, instruments int, err error) {

	_, err = f.Seek(1080, 0)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Seek(0, 0)

	tmp := make([]byte, 4)
	_, err = io.ReadFull(f, tmp)
	if err != nil {
		return "", 0, 0, err
	}
	format = string(tmp)

	switch format {

	case "M.K.":
		fallthrough
	case "FLT4":
		fallthrough
	case "M!K!":
		fallthrough
	case "4CHN":
		channels = 4
		instruments = 32			// Including the abstract instrument 0

	case "6CHN":
		channels = 6
		instruments = 32

	case "OCTA":
		fallthrough
	case "FLT8":
		fallthrough
	case "CD81":
		fallthrough
	case "8CHN":
		channels = 8
		instruments = 32

	default:
		channels = 4
		instruments = 16
		format = ""
	}

	return format, channels, instruments, err
}


func loadmod(f *os.File) (*Modfile, error) {

	var err error

	modfile := new(Modfile)

	// Search for known file formats at location 1080 (decimal)

	modfile.Format, modfile.ChannelCount, modfile.SampleCount, err = get_format(f)

	// We'll start using buffered IO after get_format(), which uses seek

	infile := bufio.NewReader(f)

	// -----

	modfile.Title, err = load_string(infile, 20)
	if err != nil {
		return modfile, err
	}

	// -----

	modfile.Samples = append(modfile.Samples, nil)		// No sample zero

	for n := 1; n < modfile.SampleCount; n++ {

		var sample *Sample

		sample, err = load_sample_info(infile, modfile.Format == "")
		if err != nil {
			return modfile, err
		}

		modfile.Samples = append(modfile.Samples, sample)
	}

	// -----

	positions, err := infile.ReadByte()		// How long the useful part of the table is (I think)
	if err != nil {
		return modfile, err
	}

	// -----

	_, err = infile.ReadByte()				// Can "safely" ignore this byte, allegedly
	if err != nil {
		return modfile, err
	}

	// -----

	modfile.Table = make([]int, positions)

	highest_pattern := 0
	table_values := make(map[byte]bool)

	patterns_exceed_table_length := false

	for n := 0; n < 128; n++ {
		val, err := infile.ReadByte()
		if err != nil {
			return modfile, err
		}
		table_values[val] = true
		if n < len(modfile.Table) {
			modfile.Table[n] = int(val)
			if int(val) > highest_pattern {
				highest_pattern = int(val)
			}
		} else if val != 0 {
			patterns_exceed_table_length = true
		}
	}

	// -----

	if patterns_exceed_table_length {
		fmt.Printf("WARNING: patterns continue in the table past its expected length.\n")
	}

	if len(table_values) != highest_pattern + 1 {
		fmt.Printf("WARNING: some pattern numbers are not in the table.\n")
	}

	// -----

	if modfile.Format != "" {
		infile.ReadByte(); infile.ReadByte(); infile.ReadByte(); infile.ReadByte()
	}

	// -----

	modfile.Patterns = make([]*Pattern, highest_pattern + 1)

	for n := 0; n < len(modfile.Patterns); n++ {
		modfile.Patterns[n] = new(Pattern)
		modfile.Patterns[n].Lines = make([][]*Note, 64)
		for i := 0; i < 64; i++ {
			modfile.Patterns[n].Lines[i] = make([]*Note, modfile.ChannelCount)
		}
	}

	for n := 0; n < len(modfile.Patterns); n++ {				// For each pattern...
		for i := 0; i < 64; i++ {								// For each line...
			for ch := 0; ch < modfile.ChannelCount; ch++ {		// For each channel...
				modfile.Patterns[n].Lines[i][ch], err = load_note(infile)
			}
		}
	}

	// -----

	for n := 1; n < len(modfile.Samples); n++ {
		_, err = io.ReadFull(infile, modfile.Samples[n].Data)
		if err != nil {
			return modfile, err
		}
	}

	// -----

	for {
		_, err := infile.ReadByte()
		if err != nil {
			break
		}
		modfile.Unread++
	}

	return modfile, nil
}


func load_sample_info(infile *bufio.Reader, min_length_is_1 bool) (*Sample, error) {

	var err error

	sample := new(Sample)

	sample.Name, err = load_string(infile, 22)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}

	length, err := load_big_endian_16(infile)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}
	if length == 0 && min_length_is_1 {
		length = 1
	}
	sample.Data = make([]byte, length * 2)

	finetune, err := infile.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}
	sample.Finetune = int(finetune)

	volume, err := infile.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}
	sample.Volume = int(volume)

	repoffset, err := load_big_endian_16(infile)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}
	sample.RepOffset = int(repoffset)

	replength, err := load_big_endian_16(infile)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}
	sample.RepLength = int(replength)

	return sample, nil
}


func load_big_endian_16(infile *bufio.Reader) (int, error) {

	var a, b byte
	var err error

	a, err = infile.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("load_big_endian_16: %v", err)
	}

	b, err = infile.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("load_big_endian_16: %v", err)
	}

	return (int(a) << 8) + int(b), nil
}


func load_string(infile *bufio.Reader, length int) (string, error) {
	raw := make([]byte, length)
	_, err := io.ReadFull(infile, raw)
	if err != nil {
		return "", fmt.Errorf("load_string: %v", err)
	}
	return strings.TrimRight(string(raw), "\x00"), nil
}


func load_note(infile *bufio.Reader) (*Note, error) {		// TODO / FIXME
	raw := make([]byte, 4)
	_, err := io.ReadFull(infile, raw)
	if err != nil {
		return nil, fmt.Errorf("load_note: %v", err)
	}

	note := new(Note)

	note.Sample = int((raw[0] & 0xf0) | (raw[2] >> 4))		// Make a new byte out of left 4 bits of 1st byte and left 4 bits of 3rd byte
	note.Period = 256 * int(raw[0] & 0x0f) + int(raw[1])	// A 12-bit value comprised of the right 4 bits of 1st byte and all the 2nd byte
	note.Effect = int(raw[2] & 0x0f)						// Value in range 0-15, from the right 4 bits of 3rd byte
	note.Parameter = int(raw[3])							// The 4th byte

	return note, nil
}
