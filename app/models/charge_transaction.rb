# frozen_string_literal: true

class ChargeTransaction
  include Mongoid::Document
  include Mongoid::Timestamps
  include Teamify

  CHARGE_TYPE_ONE_TIME_PAYMENT = 'one_time_payment'
  CHARGE_TYPE_SUBSCRIPTION = 'subscription'

  field :amount, type: Float
  field :charge_type, type: String
  # Plan or package
  field :item, type: String
  field :source, type: String, default: 'stripe'
  field :event_source, type: Hash

  validates_presence_of :amount
  validates_numericality_of :amount, only_integer: false
  validates_presence_of :charge_type
  validates_presence_of :item
  validates_presence_of :source

  belongs_to :user, index: true
end
