# AWS S3 with EKS Pod Identity

servtuner stores bundles in S3 with **no keys anywhere**: bench and the api use the AWS SDK's default
credential chain, which picks up what the EKS Pod Identity Agent injects. The same setting
covers IRSA, or an instance role for the subprocess runner on EC2.

## Chart values

```yaml
storage:
  backend: s3
  s3:
    region: <region>
    bucket: <bucket>
    prefix: runs/
    forcePathStyle: false        # AWS uses virtual-hosted addressing
    checksumMode: when_supported # AWS accepts the SDK's default checksums
    credentials: {source: ambient}
tls:
  hops:
    objectStore: mtls            # AWS S3 is HTTPS; no client certificate is sent to AWS
```

Leave `storage.s3.endpoint` empty for AWS. With an endpoint (RustFS, Ceph RGW, SeaweedFS, Garage), the
`objectStore` hop is `mtls` (HTTPS, client certificate) or `platform` (plain HTTP), as for every hop.

## What the cluster needs

1. The **EKS Pod Identity Agent** add-on.
2. Two IAM roles, trusted by `pods.eks.amazonaws.com`:
   - **bench** writes bundles;
   - **api** reads them.
3. Pod Identity associations, which `helm install` lists in its notes:
   - `servtuner-bench` in the runs namespace → the bench role;
   - `servtuner-api` in the release namespace → the api role.
   Create them with Terraform, eksctl, the console, or the chart's optional ACK resources
   (`storage.s3.aws.ack.enabled`, which needs the ACK eks-controller).

## Least-privilege policies

Scoped to the bucket and prefix. Replace `<bucket>` and `<prefix>` (for example `runs/`).

bench:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": ["s3:PutObject", "s3:AbortMultipartUpload", "s3:GetObject"],
     "Resource": "arn:aws:s3:::<bucket>/<prefix>*"},
    {"Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::<bucket>",
     "Condition": {"StringLike": {"s3:prefix": "<prefix>*"}}}
  ]
}
```

api (add `s3:DeleteObject` only when retention is enabled):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::<bucket>/<prefix>*"},
    {"Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::<bucket>",
     "Condition": {"StringLike": {"s3:prefix": "<prefix>*"}}}
  ]
}
```

**KMS-encrypted bucket:** both roles also need `kms:GenerateDataKey` and `kms:Decrypt` on the bucket's key.

## How it is tested

- Go (`bench/internal/storage/credentials_test.go`) and Python (`api/tests/test_s3_credentials.py`) run a
  fake container-credentials endpoint (`AWS_CONTAINER_CREDENTIALS_FULL_URI` and a token file, as the agent
  sets them) and assert that the SDK fetched its keys and signed with them.
- The same `BundleStore` suite runs against RustFS and SeaweedFS in containers, with both checksum modes.
- A real association on EKS is not tested here.

## Infrastructure prices (`analysis.infra_pricing.source: aws`)

With the `aws` price source, `servtuner run` queries the AWS Price List Query API once, at the start of the
run, for the instance type in the target snapshot, and writes `pricing/infra.json`. The analysis prices only from that copy. Add to the **bench** role:

```json
{
  "Effect": "Allow",
  "Action": "pricing:GetProducts",
  "Resource": "*"
}
```

The Price List API has no resource-level permissions, so `Resource` is `*`. It is served only from
`us-east-1`, `eu-central-1` and `ap-south-1`; bench calls the us-east-1 endpoint and passes the nodes'
region in the `regionCode` filter, which prices any region. The same default credential chain is used,
so Pod Identity covers it. Without the permission the run still succeeds, and the analysis says the
replica is unpriced, with the reason.
