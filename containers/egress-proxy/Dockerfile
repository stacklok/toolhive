# Use Alpine Linux 3.22.0 for minimal footprint
FROM alpine:3.22.0

# Install squid and create necessary directories
RUN apk add --no-cache squid \
    && mkdir -p /var/cache/squid /var/log/squid \
    && chown -R squid:squid /var/cache/squid /var/log/squid /var/run/squid \
    && chmod 750 /var/cache/squid /var/log/squid

# Remove default squid config to allow runtime configuration
RUN rm -f /etc/squid/squid.conf

# Set proper ownership for squid directories and ensure write permissions
RUN chown -R squid:squid /etc/squid /var/run/squid \
    && chmod 755 /var/run/squid

# Expose squid port
EXPOSE 3128

# Health check - check if squid process is running using basic shell
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD ps aux | grep -v grep | grep squid > /dev/null || exit 1

# Switch to non-root user
USER squid

# Use ENTRYPOINT for the main process
ENTRYPOINT ["squid", "-N", "-d", "1"]
