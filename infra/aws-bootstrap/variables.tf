variable "aws_region" {
  type    = string
  default = "us-east-2"
}

variable "name_prefix" {
  type    = string
  default = "portpact"
}

variable "vpc_cidr" {
  type    = string
  default = "10.42.0.0/16"
}

variable "private_subnet_cidrs" {
  type    = list(string)
  default = ["10.42.0.0/20", "10.42.16.0/20"]
}

variable "gateway_instance_type" {
  type    = string
  default = "t3.micro"
}

variable "db_instance_class" {
  type    = string
  default = "db.t4g.micro"
}

variable "db_name" {
  type    = string
  default = "postgres"
}

variable "db_username" {
  type    = string
  default = "postgres"
}

variable "db_password" {
  type      = string
  sensitive = true
}

variable "broker_api_key" {
  type        = string
  sensitive   = true
  description = "API key for authenticating requests to the broker Lambda"
}
