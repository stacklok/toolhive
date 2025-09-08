package session

// ProxySessionFactory returns a Factory that creates ProxySession instances.
// This is a convenience function that wraps NewProxySession to return the Session interface.
func ProxySessionFactory() Factory {
	return func(id string) Session {
		return NewProxySession(id)
	}
}

// SSESessionFactory returns a Factory that creates SSESession instances.
// This is a convenience function that wraps NewSSESession to return the Session interface.
func SSESessionFactory() Factory {
	return func(id string) Session {
		return NewSSESession(id)
	}
}

// StreamableSessionFactory returns a Factory that creates StreamableSession instances.
// This is a convenience function that wraps NewStreamableSession to return the Session interface.
func StreamableSessionFactory() Factory {
	return func(id string) Session {
		return NewStreamableSession(id)
	}
}

// TypedSessionFactory returns a Factory that creates sessions of the specified type.
// This reuses the same logic as NewTypedManager but returns just the factory function.
func TypedSessionFactory(sessionType SessionType) Factory {
	return func(id string) Session {
		switch sessionType {
		case SessionTypeSSE:
			return NewSSESession(id)
		case SessionTypeMCP:
			return NewProxySession(id)
		case SessionTypeStreamable:
			return NewTypedProxySession(id, sessionType)
		default:
			return NewTypedProxySession(id, sessionType)
		}
	}
}
