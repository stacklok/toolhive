# MCP Server Registry Inclusion Heuristics

## Overview

This document defines the criteria for including MCP (Model Context Protocol) servers in the ToolHive Registry. The goal is to establish a curated, community-auditable list of high-quality MCP servers through clear, observable, and objective criteria.

## Heuristics

### Open Source Requirements
- Must be fully open source with no exceptions
- Source code must be publicly accessible
- Must use permissive licenses (Apache, MIT, BSD, etc.)

### Security
- Software provenance verification (Sigstore, GitHub Attestations)
- SLSA compliance level assessment
- Pinned dependencies and GitHub Actions
- Published Software Bill of Materials (SBOMs)

### Continuous Integration
- Automated dependency updates (Dependabot, Renovate, etc.)
- Automated security scanning
- CVE monitoring
- Code linting and quality checks

### Repository Metrics
- Repository stars and forks
- Commit frequency and recency
- Contributor activity
- Issue and PR statistics

### API Compliance
- Full MCP API specification support
- Implementation of all required endpoints (tools, resources, etc.)
- Protocol version compatibility

### Tool Stability
- Version consistency
- Breaking change frequency
- Backward compatibility maintenance

### Code Quality
- Presence of automated tests
- Test coverage percentage
- Quality CI/CD implementation
- Code review practices

### Documentation
- Basic project documentation
- API documentation
- Deployment and operation guides
- Regular documentation updates

### Release Process
- Established CI-based release process
- Regular release cadence
- Semantic versioning compliance
- Maintained changelog

### Community Health

#### Responsiveness
- Active maintainer engagement
- Regular commit activity
- Timely issue and PR responses (issues open 3-4 weeks without response is a red flag)
- Bug resolution rate
- User support quality

#### Community Strength
- Project backing (individual vs. organizational)
- Number of active maintainers
- Contributor diversity
- Corporate or foundation support
- Governance model maturity

### Security Requirements

#### Authentication & Authorization
- Secure authentication mechanisms
- Proper authorization controls
- Standard security protocol support (OAuth, TLS)

#### Data Protection
- Encryption for data in transit and at rest
- Proper sensitive information handling

#### Security Practices
- Clear incident response channels
- Security issue reporting mechanisms (email, GHSA, etc.)

## Future Considerations

### Automated vs Manual Checks
- Balance between automated checks (e.g., CI/CD, security scans) and manual reviews (e.g., community health, documentation quality)
- Automated checks for basic compliance (e.g., license, API support)
- Manual reviews for nuanced aspects (e.g., community strength, documentation quality)

### Scoring System
- **Required**: Essential attributes (significant penalty if missing)
- **Expected**: Typical well-executed project attributes (moderate score impact)
- **Recommended**: Good practice indicators (positive contribution)
- **Bonus**: Excellence demonstrators (pure positive, no penalty for absence)

### Tiered Classifications
- "Verified" vs "Experimental/Community" designations
- Minimum threshold requirements (stars, maintainers, community indicators)
- Regular re-evaluation frequency for automated checks
