package structs

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

const (
	// VariablesApplyRPCMethod is the RPC method for upserting or deleting a
	// variable by its namespace and path, with optional conflict detection.
	//
	// Args: VariablesApplyRequest
	// Reply: VariablesApplyResponse
	VariablesApplyRPCMethod = "Variables.Apply"

	// VariablesListRPCMethod is the RPC method for listing variables within
	// Nomad.
	//
	// Args: VariablesListRequest
	// Reply: VariablesListResponse
	VariablesListRPCMethod = "Variables.List"

	// VariablesGetServiceRPCMethod is the RPC method for fetching a variable
	// according to its namepace and path.
	//
	// Args: VariablesByNameRequest
	// Reply: VariablesByNameResponse
	VariablesReadRPCMethod = "Variables.Read"

	// maxVariableSize is the maximum size of the unencrypted contents of a
	// variable. This size is deliberately set low and is not configurable, to
	// discourage DoS'ing the cluster
	maxVariableSize = 16384
)

// VariableMetadata is the metadata envelope for a Variable, it is the list
// object and is shared data between an VariableEncrypted and a
// VariableDecrypted object.
type VariableMetadata struct {
	Namespace   string
	Path        string
	CreateIndex uint64
	CreateTime  int64
	ModifyIndex uint64
	ModifyTime  int64
}

// VariableEncrypted structs are returned from the Encrypter's encrypt
// method. They are the only form that should ever be persisted to storage.
type VariableEncrypted struct {
	VariableMetadata
	VariableData
}

// VariableData is the secret data for a Variable
type VariableData struct {
	Data  []byte // includes nonce
	KeyID string // ID of root key used to encrypt this entry
}

// VariableDecrypted structs are returned from the Encrypter's decrypt
// method. Since they contains sensitive material, they should never be
// persisted to disk.
type VariableDecrypted struct {
	VariableMetadata
	Items VariableItems
}

// VariableItems are the actual secrets stored in a variable. They are always
// encrypted and decrypted as a single unit.
type VariableItems map[string]string

func (svi VariableItems) Size() uint64 {
	var out uint64
	for k, v := range svi {
		out += uint64(len(k))
		out += uint64(len(v))
	}
	return out
}

// Equals checks both the metadata and items in a VariableDecrypted struct
func (v1 VariableDecrypted) Equals(v2 VariableDecrypted) bool {
	return v1.VariableMetadata.Equals(v2.VariableMetadata) &&
		v1.Items.Equals(v2.Items)
}

// Equals is a convenience method to provide similar equality checking syntax
// for metadata and the VariablesData or VariableItems struct
func (sv VariableMetadata) Equals(sv2 VariableMetadata) bool {
	return sv == sv2
}

// Equals performs deep equality checking on the cleartext items of a
// VariableDecrypted. Uses reflect.DeepEqual
func (i1 VariableItems) Equals(i2 VariableItems) bool {
	return reflect.DeepEqual(i1, i2)
}

// Equals checks both the metadata and encrypted data for a VariableEncrypted
// struct
func (v1 VariableEncrypted) Equals(v2 VariableEncrypted) bool {
	return v1.VariableMetadata.Equals(v2.VariableMetadata) &&
		v1.VariableData.Equals(v2.VariableData)
}

// Equals performs deep equality checking on the encrypted data part of a
// VariableEncrypted
func (d1 VariableData) Equals(d2 VariableData) bool {
	return d1.KeyID == d2.KeyID &&
		bytes.Equal(d1.Data, d2.Data)
}

func (sv VariableDecrypted) Copy() VariableDecrypted {
	return VariableDecrypted{
		VariableMetadata: sv.VariableMetadata,
		Items:            sv.Items.Copy(),
	}
}

func (sv VariableItems) Copy() VariableItems {
	out := make(VariableItems, len(sv))
	for k, v := range sv {
		out[k] = v
	}
	return out
}

func (sv VariableEncrypted) Copy() VariableEncrypted {
	return VariableEncrypted{
		VariableMetadata: sv.VariableMetadata,
		VariableData:     sv.VariableData.Copy(),
	}
}

func (sv VariableData) Copy() VariableData {
	out := make([]byte, len(sv.Data))
	copy(out, sv.Data)
	return VariableData{
		Data:  out,
		KeyID: sv.KeyID,
	}
}

func (sv VariableDecrypted) Validate() error {

	if len(sv.Path) == 0 {
		return fmt.Errorf("variable requires path")
	}
	parts := strings.Split(sv.Path, "/")
	switch {
	case len(parts) == 1 && parts[0] == "nomad":
		return fmt.Errorf("\"nomad\" is a reserved top-level directory path, but you may write variables to \"nomad/jobs\" or below")
	case len(parts) >= 2 && parts[0] == "nomad" && parts[1] != "jobs":
		return fmt.Errorf("only paths at \"nomad/jobs\" or below are valid paths under the top-level \"nomad\" directory")
	}

	if len(sv.Items) == 0 {
		return errors.New("empty variables are invalid")
	}
	if sv.Items.Size() > maxVariableSize {
		return errors.New("variables are limited to 16KiB in total size")
	}
	if sv.Namespace == AllNamespacesSentinel {
		return errors.New("can not target wildcard (\"*\")namespace")
	}
	return nil
}

func (sv *VariableDecrypted) Canonicalize() {
	if sv.Namespace == "" {
		sv.Namespace = DefaultNamespace
	}
}

// GetNamespace returns the variable's namespace. Used for pagination.
func (sv *VariableMetadata) Copy() *VariableMetadata {
	var out VariableMetadata = *sv
	return &out
}

// GetNamespace returns the variable's namespace. Used for pagination.
func (sv VariableMetadata) GetNamespace() string {
	return sv.Namespace
}

// GetID returns the variable's path. Used for pagination.
func (sv VariableMetadata) GetID() string {
	return sv.Path
}

// GetCreateIndex returns the variable's create index. Used for pagination.
func (sv VariableMetadata) GetCreateIndex() uint64 {
	return sv.CreateIndex
}

// VariablesQuota is used to track the total size of variables entries per
// namespace. The total length of Variable.EncryptedData in bytes will be added
// to the VariablesQuota table in the same transaction as a write, update, or
// delete. This tracking effectively caps the maximum size of variables in a
// given namespace to MaxInt64 bytes.
type VariablesQuota struct {
	Namespace   string
	Size        int64
	CreateIndex uint64
	ModifyIndex uint64
}

func (svq *VariablesQuota) Copy() *VariablesQuota {
	if svq == nil {
		return nil
	}
	nq := new(VariablesQuota)
	*nq = *svq
	return nq
}

// ---------------------------------------
// RPC and FSM request/response objects

// VarOp constants give possible operations available in a transaction.
type VarOp string

const (
	VarOpSet       VarOp = "set"
	VarOpDelete    VarOp = "delete"
	VarOpDeleteCAS VarOp = "delete-cas"
	VarOpCAS       VarOp = "cas"
)

// VarOpResult constants give possible operations results from a transaction.
type VarOpResult string

const (
	VarOpResultOk       VarOpResult = "ok"
	VarOpResultConflict VarOpResult = "conflict"
	VarOpResultRedacted VarOpResult = "conflict-redacted"
	VarOpResultError    VarOpResult = "error"
)

// VariablesApplyRequest is used by users to operate on the variable store
type VariablesApplyRequest struct {
	Op  VarOp              // Operation to be performed during apply
	Var *VariableDecrypted // Variable-shaped request data
	WriteRequest
}

// VariablesApplyResponse is sent back to the user to inform them of success or failure
type VariablesApplyResponse struct {
	Op       VarOp              // Operation performed
	Input    *VariableDecrypted // Input supplied
	Result   VarOpResult        // Return status from operation
	Error    error              // Error if any
	Conflict *VariableDecrypted // Conflicting value if applicable
	Output   *VariableDecrypted // Operation Result if successful; nil for successful deletes
	WriteMeta
}

func (r *VariablesApplyResponse) IsOk() bool {
	return r.Result == VarOpResultOk
}

func (r *VariablesApplyResponse) IsConflict() bool {
	return r.Result == VarOpResultConflict || r.Result == VarOpResultRedacted
}

func (r *VariablesApplyResponse) IsError() bool {
	return r.Result == VarOpResultError
}

func (r *VariablesApplyResponse) IsRedacted() bool {
	return r.Result == VarOpResultRedacted
}

// VarApplyStateRequest is used by the FSM to modify the variable store
type VarApplyStateRequest struct {
	Op  VarOp              // Which operation are we performing
	Var *VariableEncrypted // Which directory entry
	WriteRequest
}

// VarApplyStateResponse is used by the FSM to inform the RPC layer of success or failure
type VarApplyStateResponse struct {
	Op            VarOp              // Which operation were we performing
	Result        VarOpResult        // What happened (ok, conflict, error)
	Error         error              // error if any
	Conflict      *VariableEncrypted // conflicting variable if applies
	WrittenSVMeta *VariableMetadata  // for making the VariablesApplyResponse
	WriteMeta
}

func (r *VarApplyStateRequest) ErrorResponse(raftIndex uint64, err error) *VarApplyStateResponse {
	return &VarApplyStateResponse{
		Op:        r.Op,
		Result:    VarOpResultError,
		Error:     err,
		WriteMeta: WriteMeta{Index: raftIndex},
	}
}

func (r *VarApplyStateRequest) SuccessResponse(raftIndex uint64, meta *VariableMetadata) *VarApplyStateResponse {
	return &VarApplyStateResponse{
		Op:            r.Op,
		Result:        VarOpResultOk,
		WrittenSVMeta: meta,
		WriteMeta:     WriteMeta{Index: raftIndex},
	}
}

func (r *VarApplyStateRequest) ConflictResponse(raftIndex uint64, cv *VariableEncrypted) *VarApplyStateResponse {
	var cvCopy VariableEncrypted
	if cv != nil {
		// make a copy so that we aren't sending
		// the live state store version
		cvCopy = cv.Copy()
	}
	return &VarApplyStateResponse{
		Op:        r.Op,
		Result:    VarOpResultConflict,
		Conflict:  &cvCopy,
		WriteMeta: WriteMeta{Index: raftIndex},
	}
}

func (r *VarApplyStateResponse) IsOk() bool {
	return r.Result == VarOpResultOk
}

func (r *VarApplyStateResponse) IsConflict() bool {
	return r.Result == VarOpResultConflict
}

func (r *VarApplyStateResponse) IsError() bool {
	// FIXME: This is brittle and requires immense faith that
	// the response is properly managed.
	return r.Result == VarOpResultError
}

type VariablesListRequest struct {
	QueryOptions
}

type VariablesListResponse struct {
	Data []*VariableMetadata
	QueryMeta
}

type VariablesReadRequest struct {
	Path string
	QueryOptions
}

type VariablesReadResponse struct {
	Data *VariableDecrypted
	QueryMeta
}
