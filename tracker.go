package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	w "github.com/fohristiwhirl/wavmaker"
)

// --------------------------------------------------------------------------------------------------

type Modfile struct {
	Title			string
	Format			string
	ChannelCount	int
	SampleCount		int				// 16 or 32 (I'm including the abstract sample 0)
	Table			[]int			// Will be the required size only, not 128
	Samples			[]*Sample
	Patterns		[]*Pattern
	Filesize		int64
	Unread			int
}

func (self *Modfile) PrintSummary() {

	sample_length_sum := 0
	for n := 1; n < len(self.Samples); n++ {
		sample_length_sum += len(self.Samples[n].Data)
	}

	fmt.Printf("\n")
	fmt.Printf("Title: \"%v\" -- format: \"%s\" -- %v bytes of sample data\n", self.Title, self.Format, sample_length_sum)
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

// Sometimes the parameter value is valid as is, but sometimes the left 4 and right 4 bits
// have their own separate meanings...

func (self *Note) ParameterLeft() int {
	return self.Parameter >> 4
}

func (self *Note) ParameterRight() int {
	return self.Parameter & 0x0f
}

// --------------------------------------------------------------------------------------------------

type Channel struct {
	Note
	Volume			int
	SamplePosition	uint32
	Period			int				// Set to 0 when not playing a note.
}

// --------------------------------------------------------------------------------------------------

type Sample struct {
	Name			string
	Finetune		int
	Volume			int
	RepOffset		int
	RepLength		int

	Length			int				// In "words" - set at the time of reading the metadata; 0 or 1 are considered empty.
	Data			[]byte			// Twice the nominal "length"

	Wav				map[int]*w.WAV
}

func NewSample() *Sample {
	ret := new(Sample)
	ret.Wav = make(map[int]*w.WAV)
	return ret
}

func (self *Sample) Print() {
	fmt.Printf("%22v (%5v bytes) - ft %v, v %v, rep %v %v\n", self.Name, len(self.Data), self.Finetune, self.Volume, self.RepOffset, self.RepLength)
}

func (self *Sample) MakeWav(period int) {

	if self.Wav[period] != nil {
		return
	}

	if self.Length < 2 {									// Consider size 0 or 1 to be empty
		self.Wav[period] = w.New(0)
		return
	}

	freq := 3563219 / uint32(period)						// How many frames of the sample are played per second
	new_frame_count := 44100 * uint32(len(self.Data)) / freq

	wav := w.New(new_frame_count)

	self.Wav[period] = wav

	// Set final frame directly...

	set_frame(wav, new_frame_count - 1, byte_to_int16(self.Data[len(self.Data) - 1]))

	for n := uint32(0) ; n <= new_frame_count - 2 ; n++ {

		index_f := (float64(n) / float64(new_frame_count - 1)) * float64(len(self.Data) - 1)
		index := uint32(index_f)

		interpolate_fraction := index_f - float64(index)

		old_val := byte_to_int16(self.Data[index])
		old_val_next := byte_to_int16(self.Data[index] + 1)

		diff := old_val_next  - old_val

		new_val_f := float64(old_val)  + float64(diff)  * interpolate_fraction

		new_val := int16(new_val_f)

		set_frame(wav, n, new_val)
	}

}

func byte_to_int16(val byte) int16 {		// Assuming the byte is supposed to be signed... so 127 --> +32767 but 128 --> -32768

	val_as_int16 := int16(val)

	// Since the byte is supposed to be a signed val, convert like so...

	if val_as_int16 > 127 {
		val_as_int16 -= 256
	}

	return int16(val_as_int16) * 256 + int16(val_as_int16) + 128
}

func set_frame(wav *w.WAV, pos uint32, val int16) {
	wav.Set(pos, val, val)
}

// --------------------------------------------------------------------------------------------------

const (
	POSITION_JUMP = 11
	PATTERN_BREAK = 13
	SET_SPEED = 15
)

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

	wav := generate_wav(modfile)

	wav.Save(fmt.Sprintf("%s.wav", os.Args[1]))
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

	modfile.Samples = make([]*Sample, modfile.SampleCount)
	modfile.Samples[0] = nil		// No sample zero

	for n := 1; n < modfile.SampleCount; n++ {
		sample, err := load_sample_info(infile)
		if err != nil {
			return modfile, err
		}
		modfile.Samples[n] = sample
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

	// With all metadata loaded, we can now calculate an expected filesize...

	small_filesize, large_filesize := expected_filesizes(modfile)

	if small_filesize != modfile.Filesize && large_filesize != modfile.Filesize {
		return modfile, fmt.Errorf("Filesize was %v, expected %v or %v", modfile.Filesize, small_filesize, large_filesize)
	}

	// Load the samples...

	for n := 1; n < len(modfile.Samples); n++ {

		// Apply a correction for blank samples having length 1 (meaning 2 bytes)...

		if modfile.Samples[n].Length == 0 && modfile.Filesize == large_filesize {
			modfile.Samples[n].Length = 1		// Gets doubled, below
		}

		modfile.Samples[n].Data = make([]byte, modfile.Samples[n].Length * 2)
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
		instruments = 32

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


func expected_filesizes(modfile *Modfile) (int64, int64) {

	// Only valid to call once most metadata has been loaded.
	// Returns 2 values:
	//    - one for a filesize where blank samples have size 0
	//    - one for a filesize where blank samples have size 2

	const (
		TITLE = 20
		SAMPLEMETA = 30
		EXTRAMETA = 2
		TABLE = 128
		FORMAT = 4
		LINES = 64
		NOTE = 4
	)

	var blank_samples int64

	for _, sample := range modfile.Samples[1:] {
		if sample.Length == 0 {
			blank_samples++
		}
	}

	var naive int64

	naive += TITLE
	naive += (SAMPLEMETA * (int64(modfile.SampleCount) - 1))
	naive += EXTRAMETA + TABLE + FORMAT
	naive += int64(modfile.ChannelCount) * LINES * NOTE * int64(len(modfile.Patterns))
	for _, sample := range modfile.Samples[1:] {
		naive += int64(sample.Length) * 2
	}

	if modfile.Format == "" {		// The format string (probably) won't be present.
		naive -= 4
	}

	return naive, naive + blank_samples * 2
}


func load_sample_info(infile *bufio.Reader) (*Sample, error) {

	var err error

	sample := NewSample()

	sample.Name, err = load_string(infile, 22)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}

	sample.Length, err = load_big_endian_16(infile)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}

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

	sample.RepOffset, err = load_big_endian_16(infile)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}

	sample.RepLength, err = load_big_endian_16(infile)
	if err != nil {
		return nil, fmt.Errorf("load_sample_info: %v", err)
	}

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

// --------------------------------------------------------------------------------------------------

func generate_wav(modfile *Modfile) *w.WAV {

	var wavs []*w.WAV
	var channels []*Channel

	for ch := 0; ch < modfile.ChannelCount; ch++ {
		wavs = append(wavs, w.New(44100 * 60 * 20))
		channels = append(channels, new(Channel))
	}

	output_frame := uint32(0)

	ticks_per_line := 6
	so_called_bpm := 125

	next_ticks_per_line := 6
	next_bpm := 125

	table_index := 0
	current_pattern := modfile.Patterns[modfile.Table[0]]
	linenum := 0

	position_jump_happening := false
	position_jump_arg := 0

	pattern_break_happening := false
	pattern_break_arg := 0

	info := func(format_string string, args ...interface{}) {
		fmt.Printf("%2v(%2v):%2v: ", table_index, modfile.Table[table_index], linenum)
		fmt.Printf(format_string, args...)
		fmt.Printf("\n")
	}

	for {

		if table_index < len(modfile.Table) {
			current_pattern = modfile.Patterns[modfile.Table[table_index]]
		} else {
			break
		}

		line := current_pattern.Lines[linenum]

		for ch, note := range line {
			if note.Period != 0 {
				channels[ch].Period = note.Period
				channels[ch].SamplePosition = 0
				if note.Sample != 0 {
					channels[ch].Sample = note.Sample
				}
			}

			// --------------------------------------------

			if note.Effect == SET_SPEED {

				val := note.Parameter

				if val == 0 {
					info("WARNING: ignored tickrate 0")
				} else if val <= 31 {
					next_ticks_per_line = val
					info("Set tickrate to %v", next_ticks_per_line)
				} else {
					next_bpm = val
					info("Set bpm to %v", next_bpm)
				}

			}

			if note.Effect == POSITION_JUMP {

				if note.Parameter > table_index {
					position_jump_happening = true
					position_jump_arg = note.Parameter
				}
				info("Saw note effect %d (value %d)", note.Effect, note.Parameter)
				if note.Parameter <= table_index {
					info("(but ignored due to probable infinite loop)")
				}
			}

			if note.Effect == PATTERN_BREAK {
				pattern_break_happening = true
				pattern_break_arg = note.ParameterLeft() * 10 + note.ParameterRight()				// wat
				info("Saw note effect %d (value %d)", note.Effect, pattern_break_arg)
			}
		}

		// How long does this line last? We use the formula from modformat.txt (rev 4)

		lines_per_minute := 24.0 * float32(so_called_bpm) / float32(ticks_per_line)
		seconds_per_line := (1.0 / lines_per_minute) * 60.0
		frames_needed := uint32(44100.0 * seconds_per_line)

		// Set the value for each new frame in the output wavs...

		for n := uint32(0); n < frames_needed; n++ {

			output_frame++

			for ch := 0; ch < len(channels); ch++ {

				if channels[ch].Period == 0 {
					continue
				}

				sample := modfile.Samples[channels[ch].Sample]

				if sample == nil || sample.Length < 2 {
					continue
				}

				if n == 0 {
					sample.MakeWav(channels[ch].Period)
				}

				sample_wav := sample.Wav[channels[ch].Period]

				if channels[ch].SamplePosition >= sample_wav.FrameCount() {
					if sample.RepLength > 1 {
						channels[ch].SamplePosition = uint32(sample.RepOffset) * 2			// FIXME: sanity check this.
					} else {
						channels[ch].Period = 0
						continue
					}
				}

				left, right := sample_wav.Get(channels[ch].SamplePosition)

				if ch % 4 == 2 || ch % 4 == 3 {
					wavs[ch].Set(output_frame, left / 8.0, right / 4.0)
				} else {
					wavs[ch].Set(output_frame, left / 4.0, right / 8.0)
				}

				channels[ch].SamplePosition++
			}
		}

		// -----

		linenum++
		ticks_per_line = next_ticks_per_line
		so_called_bpm = next_bpm

		if pattern_break_happening {
			table_index += 1
			linenum = pattern_break_arg
			pattern_break_happening = false
			position_jump_happening = false
		}

		if position_jump_happening {
			table_index = position_jump_arg
			linenum = 0
			pattern_break_happening = false
			position_jump_happening = false
		}

		if linenum >= 64 {
			linenum = 0
			table_index += 1
		}

	}

	truncated := w.New(output_frame + 220500)

	for ch := 0; ch < len(wavs); ch++ {
		truncated.Add(0, wavs[ch], 0, truncated.FrameCount(), 1.0, 0)
	}

	return truncated
}
