package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// --------------------------------------------------------------------------------------------------

type Modfile struct {
	Title			string
	Format			string
	ChannelCount	int
	SampleCount		int
	Table			[]int
	Samples			[]*Sample
	Patterns		[]*Pattern
	Filesize		int64
	Unread			int
}

func (self *Modfile) PrintSummary() {

	fmt.Printf("\n")
	fmt.Printf("Title: \"%v\" (format: %s)\n", self.Title, self.Format)
	fmt.Printf("\n")

	sample_length_sum := 0

	for n := 1; n < len(self.Samples); n++ {
		sample_length_sum += len(self.Samples[n].Data)
	}

	fmt.Printf("%v bytes of sample data\n", sample_length_sum)

	fmt.Printf("Table:")
	for n := 0; n < len(self.Table); n++ {
		fmt.Printf(" %v", self.Table[n])
	}
	fmt.Printf("\n")

	fmt.Printf("File size: %v (%v unread bytes)\n", self.Filesize, self.Unread)
	fmt.Printf("\n")
}

func (self *Modfile) PrintAll() {

	fmt.Printf("\n")

	for _, val := range self.Table {
		fmt.Printf("Pattern %v.....\n", val)
		self.Patterns[val].Print()
	}

	self.PrintSummary()

	for n := 1; n < len(self.Samples); n++ {
		self.Samples[n].Print()
	}

	fmt.Printf("\n")
}

// --------------------------------------------------------------------------------------------------

type Pattern struct {
	Lines			[][]*Note
}

func (self *Pattern) Print() {
	for i := 0; i < len(self.Lines); i++ {
		fmt.Printf("| ")
		for ch := 0; ch < len(self.Lines[i]); ch++ {
			fmt.Printf("%3v - %3v |", self.Lines[i][ch].Sample, self.Lines[i][ch].Period)
		}
		fmt.Printf("\n")
	}
}

// --------------------------------------------------------------------------------------------------

type Note struct {
	Sample			int
	Period			int				// This determines the pitch, I think
	Effect			int
	Parameter		int
}

// --------------------------------------------------------------------------------------------------

type Sample struct {
	Name			string
	Finetune		int
	Volume			int
	RepOffset		int
	RepLength		int
	Data			[]byte
}

func (self *Sample) Print() {
	fmt.Printf("%22v (%5v bytes) - ft %v, v %v, rep %v %v\n", self.Name, len(self.Data), self.Finetune, self.Volume, self.RepOffset, self.RepLength)
}

// --------------------------------------------------------------------------------------------------

func main() {

	if len(os.Args) < 2 {
		return
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	modfile, err := load_modfile(f)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	modfile.PrintAll()
}


func load_modfile(f *os.File) (*Modfile, error) {

	var err error

	modfile := new(Modfile)

	// Make a note of the file's size...

	stats, err := f.Stat()
	if err != nil {
		return modfile, err
	}
	modfile.Filesize = stats.Size()

	// Search for known file formats at location 1080 (decimal)...

	_, err = f.Seek(1080, 0)
	if err != nil {
		return modfile, err
	}

	modfile.Format, modfile.ChannelCount, modfile.SampleCount, err = get_format(f)
	f.Seek(0, 0)

	// We'll start using buffered IO after these seek shennanigans...

	infile := bufio.NewReader(f)

	// Load title...

	modfile.Title, err = load_string(infile, 20)
	if err != nil {
		return modfile, err
	}

	// Load sample metadata...

	modfile.Samples = append(modfile.Samples, nil)		// No sample zero

	for n := 1; n < modfile.SampleCount; n++ {
		var sample *Sample
		sample, err = load_sample_info(infile, modfile.Format == "")
		if err != nil {
			return modfile, err
		}
		modfile.Samples = append(modfile.Samples, sample)
	}

	// Load position count, which is how long the useful part of the table is (I think)...

	positions, err := infile.ReadByte()
	if err != nil {
		return modfile, err
	}

	// Load an irrelevant byte that we "can safely ignore" allegedly...

	_, err = infile.ReadByte()
	if err != nil {
		return modfile, err
	}

	// Load the table of patterns to play (always 128 long regardless of actual song length)...

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

	if patterns_exceed_table_length {
		fmt.Printf("WARNING: patterns continue in the table past its expected length.\n")
	}

	if len(table_values) != highest_pattern + 1 {
		fmt.Printf("WARNING: some pattern numbers are not in the table.\n")
	}

	// If the file was found to have a 4 byte format string, skip past it...

	if modfile.Format != "" {
		infile.ReadByte(); infile.ReadByte(); infile.ReadByte(); infile.ReadByte()
	}

	// Load the pattern data...

	modfile.Patterns = make([]*Pattern, highest_pattern + 1)

	for n := 0; n < len(modfile.Patterns); n++ {
		modfile.Patterns[n] = new(Pattern)
		modfile.Patterns[n].Lines = make([][]*Note, 64)			// Always 64 lines in a pattern
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

	// Load the samples...

	for n := 1; n < len(modfile.Samples); n++ {
		_, err = io.ReadFull(infile, modfile.Samples[n].Data)
		if err != nil {
			return modfile, err
		}
	}

	// Count any unread bytes (there must be a better way, but remember we are using buffered IO)...

	for {
		_, err := infile.ReadByte()
		if err != nil {
			break
		}
		modfile.Unread++
	}

	return modfile, nil
}


func get_format(f *os.File) (format string, channels int, instruments int, err error) {

	tmp := make([]byte, 4)
	_, err = io.ReadFull(f, tmp)
	if err != nil {
		return "", 0, 0, err
	}
	format = string(tmp)

	switch format {

	case "M.K.", "FLT4", "M!K!", "4CHN":
		channels = 4
		instruments = 32			// Including the abstract instrument 0

	case "6CHN":
		channels = 6
		instruments = 32

	case "OCTA", "FLT8", "CD81", "8CHN":
		channels = 8
		instruments = 32

	default:
		channels = 4
		instruments = 16
		format = ""
	}

	return format, channels, instruments, err
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
	if sample.Finetune > 7 {			// It's a signed 4-bit value...
		sample.Finetune -= 16			// Therefore 8 means -8, 9 means -7, etc (hope I have this right)
	}

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


func load_note(infile *bufio.Reader) (*Note, error) {
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
