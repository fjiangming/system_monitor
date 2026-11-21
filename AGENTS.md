# Maintenance

Read CUSTOMIZATIONS.md before changing the program. Record source, configuration,
and test changes there. Keep this service independent of business applications.
Never add arbitrary remote shell execution or expose the Docker API over TCP.
All operator settings belong in the web interface and internal database.
Preserve unrelated changes. Deployment requires an explicit user request.
