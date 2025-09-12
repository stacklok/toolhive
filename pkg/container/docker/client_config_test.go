package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestSetupExposedPorts_SetsPorts(t *testing.T) {
	t.Parallel()

	cfg := &container.Config{}
	exposed := map[string]struct{}{
		"8080/tcp": {},
		"9090/tcp": {},
	}

	err := setupExposedPorts(cfg, exposed)
	require.NoError(t, err)
	require.NotNil(t, cfg.ExposedPorts)

	p8080, err := nat.NewPort("tcp", "8080")
	require.NoError(t, err)
	p9090, err := nat.NewPort("tcp", "9090")
	require.NoError(t, err)

	assert.Contains(t, cfg.ExposedPorts, p8080)
	assert.Contains(t, cfg.ExposedPorts, p9090)
	assert.Len(t, cfg.ExposedPorts, 2)
}

func TestSetupExposedPorts_EmptyNoChange(t *testing.T) {
	t.Parallel()

	cfg := &container.Config{}
	err := setupExposedPorts(cfg, map[string]struct{}{})
	require.NoError(t, err)
	// No ports set at all
	assert.Nil(t, cfg.ExposedPorts)
}

func TestSetupPortBindings_SetsBindings(t *testing.T) {
	t.Parallel()

	hostCfg := &container.HostConfig{}
	bindings := map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "127.0.0.1", HostPort: "8081"},
			{HostIP: "", HostPort: "8082"},
		},
	}

	err := setupPortBindings(hostCfg, bindings)
	require.NoError(t, err)

	require.NotNil(t, hostCfg.PortBindings)
	p8080, err := nat.NewPort("tcp", "8080")
	require.NoError(t, err)

	got, ok := hostCfg.PortBindings[p8080]
	require.True(t, ok)
	require.Len(t, got, 2)
	assert.Equal(t, nat.PortBinding{HostIP: "127.0.0.1", HostPort: "8081"}, got[0])
	assert.Equal(t, nat.PortBinding{HostIP: "", HostPort: "8082"}, got[1])
}

func TestConvertMounts_BindMounts(t *testing.T) {
	t.Parallel()

	in := []runtime.Mount{
		{Source: "/src1", Target: "/dst1", ReadOnly: true},
		{Source: "/src2", Target: "/dst2", ReadOnly: false},
	}
	out := convertMounts(in)

	require.Len(t, out, 2)
	assert.Equal(t, mount.TypeBind, out[0].Type)
	assert.Equal(t, "/src1", out[0].Source)
	assert.Equal(t, "/dst1", out[0].Target)
	assert.Equal(t, true, out[0].ReadOnly)

	assert.Equal(t, mount.TypeBind, out[1].Type)
	assert.Equal(t, "/src2", out[1].Source)
	assert.Equal(t, "/dst2", out[1].Target)
	assert.Equal(t, false, out[1].ReadOnly)
}

func TestCompareEnvVars_SubsetMatches(t *testing.T) {
	t.Parallel()

	existing := []string{"A=a", "B=b"}
	desired := []string{"A=a"} // subset must be OK
	assert.True(t, compareEnvVars(existing, desired))

	assert.False(t, compareEnvVars(existing, []string{"A=x"}))   // wrong value
	assert.False(t, compareEnvVars(existing, []string{"C=c"}))   // missing key
	assert.True(t, compareEnvVars(existing, []string{}))         // empty desired OK
	assert.True(t, compareEnvVars(existing, existing))           // exact match OK
	assert.True(t, compareEnvVars([]string{"A=a"}, []string{}))  // empty desired
	assert.False(t, compareEnvVars([]string{}, []string{"A=a"})) // desired not subset
}

func TestCompareLabels_SubsetMatches(t *testing.T) {
	t.Parallel()

	existing := map[string]string{"k1": "v1", "k2": "v2"}
	assert.True(t, compareLabels(existing, map[string]string{"k1": "v1"})) // subset
	assert.False(t, compareLabels(existing, map[string]string{"k1": "x"})) // wrong value
	assert.False(t, compareLabels(existing, map[string]string{"k3": "v3"}))
	assert.True(t, compareLabels(existing, map[string]string{})) // empty desired OK
}

func TestCompareHostConfig_EqualAndMismatch(t *testing.T) {
	t.Parallel()

	existing := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode:   "bridge",
				CapAdd:        []string{"CAP_A"},
				CapDrop:       []string{"ALL"},
				SecurityOpt:   []string{"seccomp:unconfined"},
				Privileged:    false,
				RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
			},
		},
	}

	desired := &container.HostConfig{
		NetworkMode:   "bridge",
		CapAdd:        []string{"CAP_A"},
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"seccomp:unconfined"},
		Privileged:    false,
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	assert.True(t, compareHostConfig(existing, desired))

	desired.Privileged = true
	assert.False(t, compareHostConfig(existing, desired))
}

func TestComparePortConfig_EqualAndMismatch(t *testing.T) {
	t.Parallel()

	// Build desired
	desiredCfg := &container.Config{}
	require.NoError(t, setupExposedPorts(desiredCfg, map[string]struct{}{
		"8080/tcp": {},
	}))
	desiredHost := &container.HostConfig{}
	require.NoError(t, setupPortBindings(desiredHost, map[string][]runtime.PortBinding{
		"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "18080"}},
	}))

	// Build existing to match desired
	p8080, err := nat.NewPort("tcp", "8080")
	require.NoError(t, err)

	existing := &container.InspectResponse{
		Config: &container.Config{
			ExposedPorts: nat.PortSet{p8080: {}},
		},
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				PortBindings: nat.PortMap{
					p8080: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "18080"}},
				},
			},
		},
	}

	assert.True(t, comparePortConfig(existing, desiredCfg, desiredHost))

	// Mismatch: different host port
	existing.HostConfig.PortBindings[p8080] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "18081"}}
	assert.False(t, comparePortConfig(existing, desiredCfg, desiredHost))
}

func TestCompareContainerConfig_AllMatch(t *testing.T) {
	t.Parallel()

	// Desired configuration
	desiredCfg := &container.Config{
		Image:        "ghcr.io/stacklok/toolhive/mcp:latest",
		Cmd:          []string{"serve"},
		Env:          []string{"A=a", "B=b"},
		Labels:       map[string]string{"toolhive": "true", "name": "w1"},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		Tty:          false,
	}
	require.NoError(t, setupExposedPorts(desiredCfg, map[string]struct{}{
		"8080/tcp": {},
	}))
	desiredHost := &container.HostConfig{
		NetworkMode:   "bridge",
		CapAdd:        []string{"NET_BIND_SERVICE"},
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"seccomp:unconfined"},
		Privileged:    false,
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: "/src1", Target: "/dst1", ReadOnly: true},
			{Type: mount.TypeBind, Source: "/src2", Target: "/dst2", ReadOnly: false},
		},
	}
	require.NoError(t, setupPortBindings(desiredHost, map[string][]runtime.PortBinding{
		"8080/tcp": {{HostIP: "", HostPort: "18080"}},
	}))

	// Existing configuration (must be a superset for env vars)
	p8080, err := nat.NewPort("tcp", "8080")
	require.NoError(t, err)

	existing := &container.InspectResponse{
		Config: &container.Config{
			Image:        desiredCfg.Image,
			Cmd:          desiredCfg.Cmd,
			Env:          []string{"A=a", "B=b", "EXTRA=x"}, // superset OK
			Labels:       map[string]string{"toolhive": "true", "name": "w1"},
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			OpenStdin:    true,
			Tty:          false,
			ExposedPorts: nat.PortSet{p8080: {}},
		},
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode:   "bridge",
				CapAdd:        []string{"NET_BIND_SERVICE"},
				CapDrop:       []string{"ALL"},
				SecurityOpt:   []string{"seccomp:unconfined"},
				Privileged:    false,
				RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
				Mounts: []mount.Mount{
					{Type: mount.TypeBind, Source: "/src1", Target: "/dst1", ReadOnly: true},
					{Type: mount.TypeBind, Source: "/src2", Target: "/dst2", ReadOnly: false},
				},
				PortBindings: nat.PortMap{
					p8080: []nat.PortBinding{{HostIP: "", HostPort: "18080"}},
				},
			},
		},
	}

	assert.True(t, compareContainerConfig(existing, desiredCfg, desiredHost))

	// Change image -> mismatch
	desiredCfg2 := *desiredCfg
	desiredCfg2.Image = "different"
	assert.False(t, compareContainerConfig(existing, &desiredCfg2, desiredHost))
}
