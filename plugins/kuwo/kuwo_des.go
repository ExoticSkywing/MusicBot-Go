package kuwo

import (
	"encoding/base64"
	"encoding/binary"
)

// Kuwo's convert_url2 endpoint uses a DES-shaped legacy transform with
// little-endian blocks and an extra zero block. It is not compatible with
// crypto/des padding or byte order.
var kuwoDESKey = [8]byte{'y', 'l', 'z', 's', 'x', 'k', 'w', 'm'}

var kuwoDESInitialPermutation = [...]int8{
	57, 49, 41, 33, 25, 17, 9, 1, 59, 51, 43, 35, 27, 19, 11, 3,
	61, 53, 45, 37, 29, 21, 13, 5, 63, 55, 47, 39, 31, 23, 15, 7,
	56, 48, 40, 32, 24, 16, 8, 0, 58, 50, 42, 34, 26, 18, 10, 2,
	60, 52, 44, 36, 28, 20, 12, 4, 62, 54, 46, 38, 30, 22, 14, 6,
}

var kuwoDESFinalPermutation = [...]int8{
	39, 7, 47, 15, 55, 23, 63, 31, 38, 6, 46, 14, 54, 22, 62, 30,
	37, 5, 45, 13, 53, 21, 61, 29, 36, 4, 44, 12, 52, 20, 60, 28,
	35, 3, 43, 11, 51, 19, 59, 27, 34, 2, 42, 10, 50, 18, 58, 26,
	33, 1, 41, 9, 49, 17, 57, 25, 32, 0, 40, 8, 48, 16, 56, 24,
}

var kuwoDESExpansion = [...]int8{
	31, 0, 1, 2, 3, 4, -1, -1, 3, 4, 5, 6, 7, 8, -1, -1,
	7, 8, 9, 10, 11, 12, -1, -1, 11, 12, 13, 14, 15, 16, -1, -1,
	15, 16, 17, 18, 19, 20, -1, -1, 19, 20, 21, 22, 23, 24, -1, -1,
	23, 24, 25, 26, 27, 28, -1, -1, 27, 28, 29, 30, 31, 30, -1, -1,
}

var kuwoDESPermutation = [...]int8{
	15, 6, 19, 20, 28, 11, 27, 16, 0, 14, 22, 25, 4, 17, 30, 9,
	1, 7, 23, 13, 31, 26, 2, 8, 18, 12, 29, 5, 21, 10, 3, 24,
}

var kuwoDESPermutedChoice1 = [...]int8{
	56, 48, 40, 32, 24, 16, 8, 0, 57, 49, 41, 33, 25, 17, 9, 1,
	58, 50, 42, 34, 26, 18, 10, 2, 59, 51, 43, 35, 62, 54, 46, 38,
	30, 22, 14, 6, 61, 53, 45, 37, 29, 21, 13, 5, 60, 52, 44, 36,
	28, 20, 12, 4, 27, 19, 11, 3,
}

var kuwoDESPermutedChoice2 = [...]int8{
	13, 16, 10, 23, 0, 4, -1, -1, 2, 27, 14, 5, 20, 9, -1, -1,
	22, 18, 11, 3, 25, 7, -1, -1, 15, 6, 26, 19, 12, 1, -1, -1,
	40, 51, 30, 36, 46, 54, -1, -1, 29, 39, 50, 44, 32, 47, -1, -1,
	43, 48, 38, 55, 33, 52, -1, -1, 45, 41, 49, 35, 28, 31, -1, -1,
}

var kuwoDESLeftShifts = [...]uint8{1, 1, 2, 2, 2, 2, 2, 2, 1, 2, 2, 2, 2, 2, 2, 1}

var kuwoDESSBoxes = [...][64]uint8{
	{
		14, 4, 3, 15, 2, 13, 5, 3, 13, 14, 6, 9, 11, 2, 0, 5,
		4, 1, 10, 12, 15, 6, 9, 10, 1, 8, 12, 7, 8, 11, 7, 0,
		0, 15, 10, 5, 14, 4, 9, 10, 7, 8, 12, 3, 13, 1, 3, 6,
		15, 12, 6, 11, 2, 9, 5, 0, 4, 2, 11, 14, 1, 7, 8, 13,
	},
	{
		15, 0, 9, 5, 6, 10, 12, 9, 8, 7, 2, 12, 3, 13, 5, 2,
		1, 14, 7, 8, 11, 4, 0, 3, 14, 11, 13, 6, 4, 1, 10, 15,
		3, 13, 12, 11, 15, 3, 6, 0, 4, 10, 1, 7, 8, 4, 11, 14,
		13, 8, 0, 6, 2, 15, 9, 5, 7, 1, 10, 12, 14, 2, 5, 9,
	},
	{
		10, 13, 1, 11, 6, 8, 11, 5, 9, 4, 12, 2, 15, 3, 2, 14,
		0, 6, 13, 1, 3, 15, 4, 10, 14, 9, 7, 12, 5, 0, 8, 7,
		13, 1, 2, 4, 3, 6, 12, 11, 0, 13, 5, 14, 6, 8, 15, 2,
		7, 10, 8, 15, 4, 9, 11, 5, 9, 0, 14, 3, 10, 7, 1, 12,
	},
	{
		7, 10, 1, 15, 0, 12, 11, 5, 14, 9, 8, 3, 9, 7, 4, 8,
		13, 6, 2, 1, 6, 11, 12, 2, 3, 0, 5, 14, 10, 13, 15, 4,
		13, 3, 4, 9, 6, 10, 1, 12, 11, 0, 2, 5, 0, 13, 14, 2,
		8, 15, 7, 4, 15, 1, 10, 7, 5, 6, 12, 11, 3, 8, 9, 14,
	},
	{
		2, 4, 8, 15, 7, 10, 13, 6, 4, 1, 3, 12, 11, 7, 14, 0,
		12, 2, 5, 9, 10, 13, 0, 3, 1, 11, 15, 5, 6, 8, 9, 14,
		14, 11, 5, 6, 4, 1, 3, 10, 2, 12, 15, 0, 13, 2, 8, 5,
		11, 8, 0, 15, 7, 14, 9, 4, 12, 7, 10, 9, 1, 13, 6, 3,
	},
	{
		12, 9, 0, 7, 9, 2, 14, 1, 10, 15, 3, 4, 6, 12, 5, 11,
		1, 14, 13, 0, 2, 8, 7, 13, 15, 5, 4, 10, 8, 3, 11, 6,
		10, 4, 6, 11, 7, 9, 0, 6, 4, 2, 13, 1, 9, 15, 3, 8,
		15, 3, 1, 14, 12, 5, 11, 0, 2, 12, 14, 7, 5, 10, 8, 13,
	},
	{
		4, 1, 3, 10, 15, 12, 5, 0, 2, 11, 9, 6, 8, 7, 6, 9,
		11, 4, 12, 15, 0, 3, 10, 5, 14, 13, 7, 8, 13, 14, 1, 2,
		13, 6, 14, 9, 4, 1, 2, 14, 11, 13, 5, 0, 1, 10, 8, 3,
		0, 11, 3, 5, 9, 4, 15, 2, 7, 8, 12, 15, 10, 7, 6, 12,
	},
	{
		13, 7, 10, 0, 6, 9, 5, 15, 8, 4, 3, 10, 11, 14, 12, 5,
		2, 11, 9, 6, 15, 12, 0, 3, 4, 1, 14, 13, 1, 2, 7, 8,
		1, 2, 12, 15, 10, 4, 0, 3, 13, 14, 6, 9, 7, 8, 9, 6,
		15, 1, 5, 12, 3, 10, 14, 5, 8, 7, 11, 0, 4, 13, 2, 11,
	},
}

func kuwoDESBitTransform(table []int8, value uint64) uint64 {
	var transformed uint64
	for destination, source := range table {
		if source >= 0 && value&(uint64(1)<<uint(source)) != 0 {
			transformed |= uint64(1) << uint(destination)
		}
	}
	return transformed
}

func kuwoDESSubkeys(key uint64) [16]uint64 {
	var subkeys [16]uint64
	transformed := kuwoDESBitTransform(kuwoDESPermutedChoice1[:], key)
	for index, shift := range kuwoDESLeftShifts {
		mask := uint64(0x100001)
		if shift == 2 {
			mask = 0x300003
		}
		transformed = ((transformed & mask) << (28 - shift)) |
			((transformed &^ mask) >> shift)
		subkeys[index] = kuwoDESBitTransform(kuwoDESPermutedChoice2[:], transformed)
	}
	return subkeys
}

func kuwoDESEncryptBlock(subkeys [16]uint64, value uint64) uint64 {
	permuted := kuwoDESBitTransform(kuwoDESInitialPermutation[:], value)
	source := [2]uint64{permuted & 0xffffffff, permuted >> 32}
	for _, subkey := range subkeys {
		right := kuwoDESBitTransform(kuwoDESExpansion[:], source[1]) ^ subkey
		var substituted uint64
		for box := 7; box >= 0; box-- {
			substituted <<= 4
			substituted |= uint64(kuwoDESSBoxes[box][byte(right>>uint(box*8))])
		}
		left := source[0]
		source[0] = source[1]
		source[1] = left ^ kuwoDESBitTransform(kuwoDESPermutation[:], substituted)
	}
	merged := (source[0] << 32) | (source[1] & 0xffffffff)
	return kuwoDESBitTransform(kuwoDESFinalPermutation[:], merged)
}

func encodeKuwoQuery(plaintext string) string {
	source := []byte(plaintext)
	blocks := len(source)/8 + 1
	encrypted := make([]byte, blocks*8)
	subkeys := kuwoDESSubkeys(binary.LittleEndian.Uint64(kuwoDESKey[:]))
	for block := 0; block < blocks; block++ {
		var input [8]byte
		start := block * len(input)
		if start < len(source) {
			copy(input[:], source[start:])
		}
		output := kuwoDESEncryptBlock(subkeys, binary.LittleEndian.Uint64(input[:]))
		binary.LittleEndian.PutUint64(encrypted[start:start+len(input)], output)
	}
	return base64.StdEncoding.EncodeToString(encrypted)
}
