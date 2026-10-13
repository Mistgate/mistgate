package agent

import (
	"errors"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/decoy"
)

func (a *Agent) setDecoy(s *session, cmd *pb.SetDecoy) {
	if cmd == nil || cmd.RequestId == "" {
		return
	}
	result := &pb.CommandResult{RequestId: cmd.RequestId}
	files := make([]decoy.File, 0, len(cmd.Files))
	for _, file := range cmd.Files {
		if file == nil {
			result.Error = "bad_params"
			s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_CommandResult{CommandResult: result}})
			return
		}
		files = append(files, decoy.File{Path: file.Path, Content: file.Contents})
	}
	if _, err := a.decoySite.Apply(cmd.Mode, cmd.BundleSha256, files); err != nil {
		if errors.Is(err, decoy.ErrInvalidBundle) {
			result.Error = "bad_params"
		} else {
			result.Error = "failed"
			a.log.Warn("decoy site update failed")
		}
		s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_CommandResult{CommandResult: result}})
		return
	}
	mode, hash := a.decoySite.State()
	result.Ok = true
	result.Params = map[string]string{"mode": mode, "sha256": hash}
	s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_CommandResult{CommandResult: result}})
}
