package ports

import (
	"strings"

	"docker-cp/internal/docker"
)

// Duplicate derives a spec for a copy of src named newName. Host ports come
// from next, volumes get a "-newName" suffix, bind mounts are kept as is and
// compose labels are dropped. Nothing is shared with src.
func Duplicate(src docker.CreateSpec, newName string, next func() int) docker.CreateSpec {
	out := src
	out.Name = newName
	out.Ports = append([]docker.PortBinding(nil), src.Ports...)
	for i := range out.Ports {
		out.Ports[i].HostPort = next()
	}
	out.Env = append([]string(nil), src.Env...)
	out.Cmd = append([]string(nil), src.Cmd...)
	out.Mounts = append([]docker.Mount(nil), src.Mounts...)
	for i, m := range out.Mounts {
		if m.Type == "volume" {
			out.Mounts[i].Source = m.Source + "-" + newName
		}
	}
	if src.Labels != nil {
		out.Labels = make(map[string]string, len(src.Labels))
		for k, v := range src.Labels {
			if !strings.HasPrefix(k, "com.docker.compose.") {
				out.Labels[k] = v
			}
		}
	}
	return out
}
