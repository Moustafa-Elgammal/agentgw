# Build from the repository root: docker build -f examples/pet-backend-python/Dockerfile .
FROM python:3.13-slim
WORKDIR /app
COPY examples/signing/python/sigverify.py /app/sigverify.py
COPY examples/pet-backend-python/server.py /app/server.py
USER nobody
EXPOSE 9090
ENTRYPOINT ["python", "-u", "/app/server.py"]
