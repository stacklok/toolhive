// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"path"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8sptr "k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/redisconfig"
)

const (
	// SessionRedisTLSCACertVolumeName is the pod volume holding the CA bundle
	// used to verify the Redis session storage server certificate.
	SessionRedisTLSCACertVolumeName = "session-redis-tls-ca"

	// SessionRedisTLSCACertMountPath is the directory where the session storage
	// Redis CA bundle is mounted. It is distinct from the embedded auth server's
	// RedisTLSCACertMountPath so both can be mounted in the same container.
	SessionRedisTLSCACertMountPath = "/etc/toolhive/session-redis-tls"

	// SessionRedisTLSCACertFileName is the file name of the mounted CA bundle.
	SessionRedisTLSCACertFileName = "ca.crt"
)

// SessionRedisTLSConfig returns the runtime TLS settings for Redis session
// storage, or nil when session storage is not Redis or TLS is not configured
// (plaintext). When a CA Secret is referenced, CACertFile points at the file
// mounted by SessionRedisTLSVolumes.
func SessionRedisTLSConfig(ss *mcpv1beta1.SessionStorageConfig) *redisconfig.TLSConfig {
	tlsCfg := sessionRedisTLS(ss)
	if tlsCfg == nil {
		return nil
	}
	rc := &redisconfig.TLSConfig{InsecureSkipVerify: tlsCfg.InsecureSkipVerify}
	if tlsCfg.CACertSecretRef != nil {
		rc.CACertFile = path.Join(SessionRedisTLSCACertMountPath, SessionRedisTLSCACertFileName)
	}
	return rc
}

// SessionRedisTLSVolumes returns the volume and mount that project the
// session storage Redis CA Secret into the container, or nil slices when no
// CA Secret is referenced.
func SessionRedisTLSVolumes(ss *mcpv1beta1.SessionStorageConfig) ([]corev1.Volume, []corev1.VolumeMount) {
	vol := sessionRedisTLSVolume(ss)
	if vol == nil {
		return nil, nil
	}
	return []corev1.Volume{*vol}, []corev1.VolumeMount{{
		Name:      SessionRedisTLSCACertVolumeName,
		MountPath: path.Join(SessionRedisTLSCACertMountPath, SessionRedisTLSCACertFileName),
		SubPath:   SessionRedisTLSCACertFileName,
		ReadOnly:  true,
	}}
}

// SessionRedisTLSVolumeNeedsUpdate reports whether the live pod volumes do not
// project the CA Secret that ss references. The RunConfig checksum already
// triggers a rollout when TLS is enabled or disabled, but the mounted file path
// is fixed, so a change to the referenced Secret name or key is only visible in
// the volume itself.
func SessionRedisTLSVolumeNeedsUpdate(live []corev1.Volume, ss *mcpv1beta1.SessionStorageConfig) bool {
	want := sessionRedisTLSVolume(ss)
	var got *corev1.Volume
	for i := range live {
		if live[i].Name == SessionRedisTLSCACertVolumeName {
			got = &live[i]
			break
		}
	}
	if want == nil || got == nil {
		return (want == nil) != (got == nil)
	}
	if got.Secret == nil {
		return true
	}
	return got.Secret.SecretName != want.Secret.SecretName ||
		!equality.Semantic.DeepEqual(got.Secret.Items, want.Secret.Items)
}

// sessionRedisTLS returns the TLS block of a Redis session storage config, or
// nil when session storage is not Redis or TLS is not configured.
func sessionRedisTLS(ss *mcpv1beta1.SessionStorageConfig) *mcpv1beta1.RedisTLSConfig {
	if ss == nil || ss.Provider != mcpv1beta1.SessionStorageProviderRedis {
		return nil
	}
	return ss.TLS
}

func sessionRedisTLSVolume(ss *mcpv1beta1.SessionStorageConfig) *corev1.Volume {
	tlsCfg := sessionRedisTLS(ss)
	if tlsCfg == nil || tlsCfg.CACertSecretRef == nil {
		return nil
	}
	return &corev1.Volume{
		Name: SessionRedisTLSCACertVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: tlsCfg.CACertSecretRef.Name,
				Items: []corev1.KeyToPath{{
					Key:  tlsCfg.CACertSecretRef.Key,
					Path: SessionRedisTLSCACertFileName,
				}},
				DefaultMode: k8sptr.To(int32(0400)),
			},
		},
	}
}
