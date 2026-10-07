package codeindex

import (
	"errors"
	"strings"
)

const (
	fragmentPrefix = "code-fragment-v1:"
	fragmentBytes  = 48 * 1024
	maxFileParts   = 256
	maxPackedFile  = maxFileParts * fragmentBytes
)

type fileFragments struct {
	Parts        []string `json:"file_parts,omitempty"`
	EncodedBytes int      `json:"encoded_bytes,omitempty"`
	EncodedSHA   string   `json:"encoded_sha256,omitempty"`
}

func packFile(file File, blobs map[string]string) (string, int, error) {
	encoded, err := encode(file)
	if err != nil {
		return "", 0, err
	}
	if len(encoded) <= maxRowBytes {
		return encoded, len(encoded), nil
	}
	if len(encoded) > maxPackedFile {
		return "", 0, errors.New("code file encoding exceeds fragment bound")
	}
	parts := fileFragments{EncodedBytes: len(encoded), EncodedSHA: digest([]byte(encoded))}
	stored := 0
	for start := 0; start < len(encoded); start += fragmentBytes {
		value := fragmentPrefix + encoded[start:min(start+fragmentBytes, len(encoded))]
		key := digest([]byte(value))
		blobs[Prefix+"blob/"+key] = value
		parts.Parts = append(parts.Parts, key)
		stored += len(value)
	}
	value, err := pack(parts)
	return value, stored + len(value), err
}

func readFile(value string, read func(string) (string, bool, error)) (File, int, error) {
	var envelope struct {
		File
		fileFragments
	}
	if err := unpack(value, &envelope); err != nil {
		return File{}, 0, err
	}
	if envelope.Parts == nil && envelope.EncodedBytes == 0 && envelope.EncodedSHA == "" {
		return envelope.File, len(value), nil
	}
	if envelope.Path != "" || len(envelope.Parts) == 0 || len(envelope.Parts) > maxFileParts || envelope.EncodedBytes <= maxRowBytes || envelope.EncodedBytes > maxPackedFile {
		return File{}, 0, errors.New("invalid code file fragment descriptor")
	}
	var encoded strings.Builder
	encoded.Grow(envelope.EncodedBytes)
	stored := len(value)
	for _, part := range envelope.Parts {
		fragment, found, err := read(Prefix + "blob/" + part)
		if err != nil {
			return File{}, 0, err
		}
		if !found || len(fragment) > len(fragmentPrefix)+fragmentBytes || !strings.HasPrefix(fragment, fragmentPrefix) || digest([]byte(fragment)) != part {
			return File{}, 0, errors.New("missing or corrupt code file fragment")
		}
		payload := strings.TrimPrefix(fragment, fragmentPrefix)
		if payload == "" || encoded.Len()+len(payload) > envelope.EncodedBytes {
			return File{}, 0, errors.New("code file fragments exceed declared size")
		}
		encoded.WriteString(payload)
		stored += len(fragment)
	}
	if encoded.Len() != envelope.EncodedBytes || digest([]byte(encoded.String())) != envelope.EncodedSHA {
		return File{}, 0, errors.New("incomplete or reordered code file fragments")
	}
	var file File
	if err := unpackBounded(encoded.String(), &file, maxPackedFile); err != nil {
		return File{}, 0, err
	}
	return file, stored, nil
}
