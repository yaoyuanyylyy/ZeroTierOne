/*
 * Copyright (c)2013-2020 ZeroTier, Inc.
 *
 * Use of this software is governed by the Business Source License included
 * in the LICENSE.TXT file in the project's root directory.
 *
 * Change Date: 2024-01-01
 *
 * On the date above, in accordance with the Business Source License, use
 * of this software will be governed by version 2.0 of the Apache License.
 */
/****/

package zerotier

// #include "../../native/GoGlue.h"
import "C"

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unsafe"
)

// Constants from node/Identity.hpp (must be the same)
const (
	IdentityTypeC25519 = 0
	IdentityTypeP384   = 1

	IdentityTypeC25519PublicKeySize  = 64
	IdentityTypeC25519PrivateKeySize = 64
	IdentityTypeP384PublicKeySize    = 114
	IdentityTypeP384PrivateKeySize   = 112
)

// Identity is precisely what it sounds like: the address and associated keys for a ZeroTier node
type Identity struct {
	address    Address
	idtype     int
	publicKey  []byte
	privateKey []byte
	cid        unsafe.Pointer // ZT_Identity
}

func identityFinalizer(obj interface{}) {
	id, _ := obj.(*Identity)
	if id != nil && uintptr(id.cid) != 0 {
		C.ZT_Identity_delete(id.cid)
	}
}

func newIdentityFromCIdentity(cid unsafe.Pointer) (*Identity, error) {
	if uintptr(cid) == 0 {
		return nil, ErrInvalidParameter
	}

	var idStrBuf [4096]byte
	idStr := C.ZT_Identity_toString(cid, (*C.char)(unsafe.Pointer(&idStrBuf[0])), 4096, 1)
	if uintptr(unsafe.Pointer(idStr)) == 0 {
		return nil, ErrInternal
	}

	id, err := NewIdentityFromString(C.GoString(idStr))
	if err != nil {
		return nil, err
	}

	id.cid = cid
	runtime.SetFinalizer(id, identityFinalizer)

	return id, nil
}

// initCIdentityPtr returns a pointer to the core ZT_Identity instance or nil/0 on error.
func (id *Identity) initCIdentityPtr() bool {
	if uintptr(id.cid) == 0 {
		idCStr := C.CString(id.String())
		defer C.free(unsafe.Pointer(idCStr))
		id.cid = C.ZT_Identity_fromString(idCStr)
		if uintptr(id.cid) == 0 {
			return false
		}
		runtime.SetFinalizer(id, identityFinalizer)
	}
	return true
}

// NewIdentity generates a new identity of the selected type
func NewIdentity(identityType int) (*Identity, error) {
	return newIdentityFromCIdentity(C.ZT_Identity_new(C.enum_ZT_Identity_Type(identityType)))
}

// NewIdentityFromString generates a new identity from its string representation.
// The private key is imported as well if it is present.
func NewIdentityFromString(s string) (*Identity, error) {
	ss := strings.Split(strings.TrimSpace(s), ":")
	if len(ss) < 3 {
		return nil, ErrInvalidParameter
	}

	var err error
	id := new(Identity)
	id.address, err = NewAddressFromString(ss[0])
	if err != nil {
		return nil, err
	}

	if ss[1] == "0" {
		id.idtype = 0
	} else if ss[1] == "1" {
		id.idtype = 1
	} else {
		return nil, ErrUnrecognizedIdentityType
	}

	switch id.idtype {

	case 0:
		id.publicKey, err = hex.DecodeString(ss[2])
		if err != nil {
			return nil, err
		}
		if len(ss) >= 4 {
			id.privateKey, err = hex.DecodeString(ss[3])
			if err != nil {
				return nil, err
			}
		}

	case 1:
		id.publicKey, err = Base32StdLowerCase.DecodeString(ss[2])
		if err != nil {
			return nil, err
		}
		if len(id.publicKey) != IdentityTypeP384PublicKeySize {
			return nil, ErrInvalidKey
		}
		if len(ss) >= 4 {
			id.privateKey, err = Base32StdLowerCase.DecodeString(ss[3])
			if err != nil {
				return nil, err
			}
			if len(id.privateKey) != IdentityTypeP384PrivateKeySize {
				return nil, ErrInvalidKey
			}
		}

	}

	return id, nil
}

// Address returns this identity's address
func (id *Identity) Address() Address { return id.address }

// HasPrivate returns true if this identity has its own private portion.
func (id *Identity) HasPrivate() bool { return len(id.privateKey) > 0 }

// PrivateKeyString returns the full identity.secret if the private key is set, or an empty string if no private key is set.
func (id *Identity) PrivateKeyString() string {
	switch id.idtype {
	case IdentityTypeC25519:
		if len(id.publicKey) == IdentityTypeC25519PublicKeySize && len(id.privateKey) == IdentityTypeC25519PrivateKeySize {
			return fmt.Sprintf("%.10x:0:%x:%x", uint64(id.address), id.publicKey, id.privateKey)
		}
	case IdentityTypeP384:
		if len(id.publicKey) == IdentityTypeP384PublicKeySize && len(id.privateKey) == IdentityTypeP384PrivateKeySize {
			return fmt.Sprintf("%.10x:1:%s:%s", uint64(id.address), Base32StdLowerCase.EncodeToString(id.publicKey), Base32StdLowerCase.EncodeToString(id.privateKey))
		}
	}
	return ""
}

// PublicKeyString returns the address and public key (identity.public contents).
// An empty string is returned if this identity is invalid or not initialized.
func (id *Identity) String() string {
	switch id.idtype {
	case IdentityTypeC25519:
		if len(id.publicKey) == IdentityTypeC25519PublicKeySize {
			return fmt.Sprintf("%.10x:0:%x", uint64(id.address), id.publicKey)
		}
	case IdentityTypeP384:
		if len(id.publicKey) == IdentityTypeP384PublicKeySize {
			return fmt.Sprintf("%.10x:1:%s", uint64(id.address), Base32StdLowerCase.EncodeToString(id.publicKey))
		}
	}
	return ""
}

// LocallyValidate performs local self-validation of this identity
func (id *Identity) LocallyValidate() bool {
	if !id.initCIdentityPtr() {
		return false
	}
	return C.ZT_Identity_validate(id.cid) != 0
}

// Sign signs a message with this identity
func (id *Identity) Sign(msg []byte) ([]byte, error) {
	if !id.initCIdentityPtr() {
		return nil, ErrInvalidKey
	}

	var dataP unsafe.Pointer
	if len(msg) > 0 {
		dataP = unsafe.Pointer(&msg[0])
	}
	var sig [96]byte
	sigLen := C.ZT_Identity_sign(id.cid, dataP, C.uint(len(msg)), unsafe.Pointer(&sig[0]), 96)
	if sigLen <= 0 {
		return nil, ErrInvalidKey
	}

	return sig[0:uint(sigLen)], nil
}

// Verify verifies a signature
func (id *Identity) Verify(msg, sig []byte) bool {
	if len(sig) == 0 || !id.initCIdentityPtr() {
		return false
	}
	var dataP unsafe.Pointer
	if len(msg) > 0 {
		dataP = unsafe.Pointer(&msg[0])
	}
	return C.ZT_Identity_verify(id.cid, dataP, C.uint(len(msg)), unsafe.Pointer(&sig[0]), C.uint(len(sig))) != 0
}

// MakeRoot generates a root spec consisting of a serialized identity and a root locator.
func (id *Identity) MakeRoot(addresses []InetAddress) ([]byte, error) {
	if len(addresses) == 0 {
		return nil, errors.New("at least one static address must be specified for a root")
	}
	if !id.initCIdentityPtr() {
		return nil, errors.New("error initializing ZT_Identity")
	}

	ss := make([]C.struct_sockaddr_storage, len(addresses))
	for i := range addresses {
		if !makeSockaddrStorage(addresses[i].IP, addresses[i].Port, &ss[i]) {
			return nil, errors.New("invalid address in address list")
		}
	}
	var buf [8192]byte
	rl := C.ZT_Identity_makeRootSpecification(id.cid, C.int64_t(TimeMs()), &ss[0], C.uint(len(ss)), unsafe.Pointer(&buf[0]), 8192)
	if rl <= 0 {
		return nil, errors.New("unable to make root specification (does identity contain a secret key?)")
	}
	return buf[0:int(rl)], nil
}

// Equals performs a deep equality test between this and another identity
func (id *Identity) Equals(id2 *Identity) bool {
	if id2 == nil {
		return id == nil
	}
	if id == nil {
		return false
	}
	return id.address == id2.address && id.idtype == id2.idtype && bytes.Equal(id.publicKey, id2.publicKey) && bytes.Equal(id.privateKey, id2.privateKey)
}

// MarshalJSON marshals this Identity in its string format (private key is never included)
func (id *Identity) MarshalJSON() ([]byte, error) {
	return []byte("\"" + id.String() + "\""), nil
}

// UnmarshalJSON unmarshals this Identity from a string
func (id *Identity) UnmarshalJSON(j []byte) error {
	var s string
	err := json.Unmarshal(j, &s)
	if err != nil {
		return err
	}
	nid, err := NewIdentityFromString(s)
	if err != nil {
		return err
	}
	*id = *nid
	return nil
}