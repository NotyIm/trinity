class ReceiverService
  # Create receiver and its logic from parameter
  # @param Receiver object
  # @param boolean true false
  def self.save(receiver)
    if !TeamPolicy.can_manage?(receiver.team, receiver.user) ||
        !TeamPolicy.can_view?(receiver.team, receiver.user)
      raise "UserCannotManageTeam"
    end

    if receiver.save! && receiver.require_verify?
      VerificationService.generate(receiver)
    end

    if receiver.provider_class.auto_assign_after_create?
      receiver.user.checks.each do |check|
        check.receivers ||= []
        check.receivers << receiver.id.to_s
        check.save!
      end
    end
  end
end
